package main

// /goal: a persisted objective the session works toward across turns.
//
// The state lives in the session file as custom entries. Every change appends
// a new entry, so the latest one on the active branch is the state, forks
// carry the goal of the branch they started from, and a stale state cannot
// overwrite a newer one. Clearing appends a cleared entry rather than
// deleting history.
//
// The loop is scheduled by the front end: /goal <objective> and /goal resume
// run inside the turn runner the interface hands the command table, and a
// plain user turn hands back to the loop when it finishes, so the model's
// work streams into the same answer block as everything else.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/session"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// goalCustomType is the CustomType of every goal state entry.
const goalCustomType = "goal"

// goalStatus is where the goal stands.
type goalStatus string

const (
	// goalActive means the loop runs, or is waiting for the reader's next
	// message to resume.
	goalActive goalStatus = "active"
	// goalPaused means a turn failed or the reader stopped it. /goal resume
	// continues it.
	goalPaused goalStatus = "paused"
	// goalBudgetLimited means the goal spent its token budget.
	goalBudgetLimited goalStatus = "budget_limited"
	// goalCleared is the terminal state: a cleared entry shadows everything
	// before it, and the session reads as having no goal.
	goalCleared goalStatus = "cleared"
)

// goalState is one persisted snapshot of the goal.
type goalState struct {
	Objective   string     `json:"objective"`
	Status      goalStatus `json:"status"`
	TokenBudget int        `json:"tokenBudget"`
	TokensUsed  int        `json:"tokensUsed"`
	Turns       int        `json:"turns"`
	Created     string     `json:"created"`
	Updated     string     `json:"updated"`
}

// The prompts the loop sends. They are user messages so they land in the
// transcript, where a reader can see what drove the work.
const (
	// goalSteering frames the objective as an autonomous goal and asks for the
	// completion report the MVP relies on instead of a verifier.
	goalSteering       = "This is a goal: work toward it autonomously — plan, make changes, and verify them. When it is done, stop and summarize what was accomplished and the evidence for it."
	goalContinuePrompt = "Continue working toward the goal."
	goalResumePrompt   = "Resume working toward the goal."
)

// goalBudget reads the token budget the config gives new goals. No goal block
// and no budget both mean unbounded.
func goalBudget(cfg *config.Config) int {
	if cfg == nil || cfg.Goal == nil {
		return 0
	}
	return cfg.Goal.TokenBudget
}

// currentGoal reads the latest goal entry on the active branch, or nil when
// the session has none.
func currentGoal(application *app.App) (*goalState, error) {
	path := application.Session().Path("")
	for i := len(path) - 1; i >= 0; i-- {
		entry := path[i]
		if entry.Type != session.TypeCustom || entry.CustomType != goalCustomType {
			continue
		}
		var g goalState
		if err := json.Unmarshal(entry.Data, &g); err != nil {
			return nil, fmt.Errorf("goal: decode the stored goal: %w", err)
		}
		return &g, nil
	}
	return nil, nil
}

// saveGoal appends the state as a new entry.
func saveGoal(application *app.App, g *goalState) error {
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("goal: encode: %w", err)
	}
	if _, err := application.Session().AppendCustomEntry(goalCustomType, data); err != nil {
		return fmt.Errorf("goal: persist: %w", err)
	}
	return nil
}

// liveGoal returns a goal in any live status, or nil when the session has
// none or the goal was cleared.
func liveGoal(application *app.App) (*goalState, error) {
	g, err := currentGoal(application)
	if err != nil {
		return nil, err
	}
	if g == nil || g.Status == goalCleared {
		return nil, nil
	}
	return g, nil
}

// activeGoal returns the goal when it is set and active.
func activeGoal(application *app.App) (*goalState, error) {
	g, err := liveGoal(application)
	if err != nil || g == nil || g.Status != goalActive {
		return nil, err
	}
	return g, nil
}

// goalCommand is /goal. The first argument picks the verb:
//
//	/goal               show the goal
//	/goal <objective>   set a goal and start working
//	/goal pause         stop the loop, keep the goal
//	/goal resume        keep working
//	/goal clear         drop the goal
func goalCommand(ctx context.Context, application *app.App, args []string, pick chooser, out io.Writer, turn turnRunner) error {
	if application == nil {
		return errors.New("/goal: no session in this mode")
	}
	if len(args) == 0 {
		return goalReport(application, out)
	}
	switch args[0] {
	case "pause":
		return goalPause(application, out)
	case "resume":
		return goalResume(ctx, application, out, turn)
	case "clear":
		return goalClear(application, out)
	}
	return goalSet(ctx, application, strings.Join(args, " "), pick, out, turn)
}

// goalReport is /goal with no argument: the objective and where it stands.
func goalReport(application *app.App, out io.Writer) error {
	g, err := currentGoal(application)
	if err != nil {
		return err
	}
	if g == nil || g.Status == goalCleared {
		fmt.Fprintln(out, "no goal; /goal <objective> starts one")
		return nil
	}
	fmt.Fprintf(out, "goal: %s\n", g.Objective)
	line := fmt.Sprintf("status: %s · %d turns · %s tokens", g.Status, g.Turns, formatTokens(g.TokensUsed))
	if g.TokenBudget > 0 {
		line += fmt.Sprintf(" · budget %s", formatTokens(g.TokenBudget))
	}
	fmt.Fprintln(out, line)
	return nil
}

// goalPause stops the loop without losing the goal.
func goalPause(application *app.App, out io.Writer) error {
	g, err := liveGoal(application)
	if err != nil {
		return err
	}
	if g == nil {
		return errors.New("/goal pause: no goal")
	}
	if g.Status != goalActive {
		fmt.Fprintf(out, "the goal is already %s; /goal resume continues it\n", g.Status)
		return nil
	}
	g.Status = goalPaused
	g.Updated = goalNow()
	if err := saveGoal(application, g); err != nil {
		return err
	}
	fmt.Fprintln(out, "goal paused; /goal resume continues it")
	return nil
}

// goalResume puts a stopped goal back to work.
func goalResume(ctx context.Context, application *app.App, out io.Writer, turn turnRunner) error {
	if turn == nil {
		return errors.New("/goal resume: no turn runner in this mode")
	}
	g, err := liveGoal(application)
	if err != nil {
		return err
	}
	if g == nil {
		return errors.New("/goal resume: no goal")
	}
	if g.Status == goalActive {
		return errors.New("/goal resume: the goal is already active")
	}
	g.Status = goalActive
	g.Updated = goalNow()
	if err := saveGoal(application, g); err != nil {
		return err
	}
	fmt.Fprintln(out, "goal resumed")
	return goalLoop(ctx, application, g, goalNote(out), turn, goalResumePrompt)
}

// goalClear drops the goal. The append-only file keeps the cleared entry, so
// the history stays readable and the next state wins.
func goalClear(application *app.App, out io.Writer) error {
	g, err := liveGoal(application)
	if err != nil {
		return err
	}
	if g == nil {
		return errors.New("/goal clear: no goal")
	}
	g.Status = goalCleared
	g.Updated = goalNow()
	if err := saveGoal(application, g); err != nil {
		return err
	}
	fmt.Fprintln(out, "goal cleared")
	return nil
}

// goalSet is /goal <objective>: record the goal, then work it.
func goalSet(ctx context.Context, application *app.App, objective string, pick chooser, out io.Writer, turn turnRunner) error {
	if turn == nil {
		return errors.New("/goal <objective>: no turn runner in this mode")
	}
	if strings.TrimSpace(objective) == "" {
		return errors.New("/goal: the objective is empty; type /goal <objective>")
	}
	existing, err := liveGoal(application)
	if err != nil {
		return err
	}
	if existing != nil {
		if pick == nil {
			return errors.New("/goal: a goal already exists; /goal clear it first")
		}
		answer := pick(tui.PickRequest{
			Title: "a goal already exists",
			Items: []tui.Choice{
				{Value: "replace", Label: "replace it", Note: existing.Objective},
				{Value: "keep", Label: "keep it"},
			},
		})
		if !answer.Taken || answer.Value != "replace" {
			fmt.Fprintln(out, "kept the existing goal; /goal shows it")
			return nil
		}
	}
	g := &goalState{
		Objective:   objective,
		Status:      goalActive,
		TokenBudget: application.GoalBudget(),
		Created:     goalNow(),
		Updated:     goalNow(),
	}
	if err := saveGoal(application, g); err != nil {
		return err
	}
	fmt.Fprintf(out, "goal set: %s\n", objective)
	return goalLoop(ctx, application, g, goalNote(out), turn, objective+"\n\n"+goalSteering)
}

// goalLoop works a goal: it runs turns until the model answers without tools,
// the budget runs out, or a turn fails. A failed turn pauses the goal, so an
// interruption does not lose it; the reader resumes with /goal resume.
//
// A stop without tools keeps the goal active: the model is done or waiting on
// the reader, and the next message in the main conversation hands back to the
// loop.
func goalLoop(ctx context.Context, application *app.App, g *goalState, note func(string), turn turnRunner, firstPrompt string) error {
	prompt := firstPrompt
	for {
		result, err := turn(ctx, func(turnCtx context.Context, emit func(wire.AgentEvent) error) (app.Result, error) {
			return application.Ask(turnCtx, wire.UserMessage(prompt), emit)
		})
		if err != nil {
			g.Status = goalPaused
			g.Updated = goalNow()
			if saveErr := saveGoal(application, g); saveErr != nil {
				return fmt.Errorf("%w; recording the pause: %v", err, saveErr)
			}
			note(fmt.Sprintf("goal paused: %v; /goal resume continues it", err))
			return err
		}
		g.TokensUsed += result.Usage.TotalTokens
		g.Turns++
		g.Updated = goalNow()
		if err := saveGoal(application, g); err != nil {
			return err
		}
		if g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget {
			g.Status = goalBudgetLimited
			g.Updated = goalNow()
			if err := saveGoal(application, g); err != nil {
				return err
			}
			note(fmt.Sprintf("goal: token budget reached (%s of %s used); /goal resume continues it", formatTokens(g.TokensUsed), formatTokens(g.TokenBudget)))
			return nil
		}
		if !usedTools(result) {
			// No tool calls: the model is done or waiting on the reader.
			return nil
		}
		note(fmt.Sprintf("goal: %d turns, %s tokens; continuing", g.Turns, formatTokens(g.TokensUsed)))
		prompt = goalContinuePrompt
	}
}

// usedTools reports whether the turn executed at least one tool. A turn that
// never did means the model answered on its own, which is the stop signal.
func usedTools(result app.Result) bool {
	for _, message := range result.Messages {
		if message.Role == wire.RoleToolResult {
			return true
		}
	}
	return false
}

// goalNote turns a writer into the single-line notice callback the loop uses.
func goalNote(out io.Writer) func(string) {
	return func(line string) { fmt.Fprintln(out, line) }
}

// goalNow is the timestamp the state stores.
func goalNow() string { return time.Now().UTC().Format(time.RFC3339) }

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool/fs"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// goalTurn runs the loop through a plain runner, the way tuiTurn does without
// a screen: it streams nothing and returns what the run produced.
func goalTurn() turnRunner {
	return func(ctx context.Context, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error) {
		return run(ctx, func(event wire.AgentEvent) error { return nil })
	}
}

// runGoalCommand dispatches a /goal line with a turn runner and a picker.
func runGoalCommand(t *testing.T, application *app.App, pick chooser, turn turnRunner, line string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	done, err := replCommand(context.Background(), application, nil, line, models{}, newSessionPicker(application, ""), pick, emptyPrompts(), out, render, mcpState{}, nil, turn)
	if done {
		t.Fatalf("%s ended the session", line)
	}
	return out.String(), err
}

// goalApp is commandApp with a token budget for new goals.
func goalApp(t *testing.T, manager *session.Manager, cwd string, budget int, responses ...faux.Response) *app.App {
	t.Helper()
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     faux.New(responses...),
		SystemPrompt: assembly.NewPrompts("", false, true).Source().Prompt,
		Tools:        app.StaticTools(fs.Tools(fs.Options{Cwd: cwd})),
		GoalBudget:   budget,
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application
}

// TestGoalReportsNoGoal: the summary answers the empty state instead of
// pretending there is one.
func TestGoalReportsNoGoal(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	printed, err := runCommand(t, application, "/goal")
	if err != nil {
		t.Fatalf("/goal: %v", err)
	}
	if !strings.Contains(printed, "no goal") {
		t.Fatalf("output = %q, want the empty state", printed)
	}
}

// TestGoalObjectiveNeedsATurnRunner: without a place to stream, the command
// refuses before it writes anything.
func TestGoalObjectiveNeedsATurnRunner(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	_, err := runCommand(t, application, "/goal ship the feature")
	if err == nil || !strings.Contains(err.Error(), "no turn runner") {
		t.Fatalf("err = %v, want a no-turn-runner refusal", err)
	}
	g, err := currentGoal(application)
	if err != nil {
		t.Fatalf("currentGoal: %v", err)
	}
	if g != nil {
		t.Fatalf("goal = %+v, want nothing persisted", g)
	}
}

// TestGoalSetRunsUntilTheModelStops: one turn with tools, one without; the
// goal stays active and waits for the reader.
func TestGoalSetRunsUntilTheModelStops(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd,
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "ls", json.RawMessage(`{"path":"."}`))}},
		faux.Response{Text: "the feature ships"},
		faux.Response{Text: "done"},
	)
	printed, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship the feature")
	if err != nil {
		t.Fatalf("/goal: %v", err)
	}
	if !strings.Contains(printed, "goal set: ship the feature") {
		t.Fatalf("output = %q, want the goal set line", printed)
	}
	if !strings.Contains(printed, "goal: 1 turns") {
		t.Fatalf("output = %q, want the progress notice", printed)
	}
	g, err := currentGoal(application)
	if err != nil {
		t.Fatalf("currentGoal: %v", err)
	}
	if g == nil {
		t.Fatal("no goal after /goal")
	}
	if g.Objective != "ship the feature" || g.Status != goalActive || g.Turns != 2 {
		t.Fatalf("goal = %+v, want the objective active after two turns", g)
	}
	// The objective is a user message, so the transcript shows what the goal is.
	found := false
	for _, message := range application.Messages() {
		if message.Role == wire.RoleUser && strings.Contains(message.Text(), "ship the feature") {
			found = true
		}
	}
	if !found {
		t.Fatal("the transcript does not carry the objective as a user message")
	}
}

// TestGoalPauseStopsTheLoop: pausing keeps the goal and names the way back.
func TestGoalPauseStopsTheLoop(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "done"})
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	printed, err := runCommand(t, application, "/goal pause")
	if err != nil {
		t.Fatalf("/goal pause: %v", err)
	}
	if !strings.Contains(printed, "goal paused") {
		t.Fatalf("output = %q, want the pause report", printed)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Status != goalPaused {
		t.Fatalf("status = %q, want paused", g.Status)
	}
}

// TestGoalResumeRunsAgain: resume puts a paused goal back to work.
func TestGoalResumeRunsAgain(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd,
		faux.Response{Text: "done"},
		faux.Response{Text: "working again"},
	)
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	if _, err := runCommand(t, application, "/goal pause"); err != nil {
		t.Fatalf("/goal pause: %v", err)
	}
	printed, err := runGoalCommand(t, application, nil, goalTurn(), "/goal resume")
	if err != nil {
		t.Fatalf("/goal resume: %v", err)
	}
	if !strings.Contains(printed, "goal resumed") {
		t.Fatalf("output = %q, want the resume report", printed)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Status != goalActive || g.Turns != 2 {
		t.Fatalf("goal = %+v, want active after a second turn", g)
	}
}

// TestGoalResumeRefusesWhenActive: resume is not a no-op spelling.
func TestGoalResumeRefusesWhenActive(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "done"})
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	_, err := runGoalCommand(t, application, nil, goalTurn(), "/goal resume")
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("err = %v, want the already-active refusal", err)
	}
}

// TestGoalClearDropsTheGoal: after clearing, the session reads as having none.
func TestGoalClearDropsTheGoal(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "done"})
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	printed, err := runCommand(t, application, "/goal clear")
	if err != nil {
		t.Fatalf("/goal clear: %v", err)
	}
	if !strings.Contains(printed, "goal cleared") {
		t.Fatalf("output = %q, want the clear report", printed)
	}
	g, err := liveGoal(application)
	if err != nil {
		t.Fatalf("liveGoal: %v", err)
	}
	if g != nil {
		t.Fatalf("goal = %+v, want none after clearing", g)
	}
}

// TestGoalSetRefusesToSilentlyReplace: with no picker, a second objective is
// an error, not a silent overwrite.
func TestGoalSetRefusesToSilentlyReplace(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "done"})
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal first"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	_, err := runGoalCommand(t, application, nil, goalTurn(), "/goal second")
	if err == nil || !strings.Contains(err.Error(), "clear it first") {
		t.Fatalf("err = %v, want the replace refusal", err)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Objective != "first" {
		t.Fatalf("objective = %q, want the first one kept", g.Objective)
	}
}

// TestGoalSetReplacesThroughTheMenu: the picker is the one place a second
// objective may win, and closing it keeps the old one.
func TestGoalSetReplacesThroughTheMenu(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd,
		faux.Response{Text: "done"},
		faux.Response{Text: "done"},
	)
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal first"); err != nil {
		t.Fatalf("/goal: %v", err)
	}

	closed := func(req tui.PickRequest) tui.PickAnswer { return tui.PickAnswer{} }
	printed, err := runGoalCommand(t, application, closed, goalTurn(), "/goal second")
	if err != nil {
		t.Fatalf("/goal second: %v", err)
	}
	if !strings.Contains(printed, "kept the existing goal") {
		t.Fatalf("output = %q, want the kept report", printed)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Objective != "first" {
		t.Fatalf("objective = %q, want the first one kept", g.Objective)
	}

	var asked tui.PickRequest
	replace := func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		return tui.PickAnswer{Value: "replace", Taken: true}
	}
	if _, err := runGoalCommand(t, application, replace, goalTurn(), "/goal second"); err != nil {
		t.Fatalf("/goal second: %v", err)
	}
	if asked.Title != "a goal already exists" {
		t.Fatalf("the menu is titled %q", asked.Title)
	}
	g, err = liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Objective != "second" || g.Status != goalActive {
		t.Fatalf("goal = %+v, want the replacement active", g)
	}
}

// TestGoalBudgetStopsTheLoop: reaching the budget is a soft stop, not a loss.
func TestGoalBudgetStopsTheLoop(t *testing.T) {
	cwd := t.TempDir()
	application := goalApp(t, memoryAt(t, cwd), cwd, 5,
		faux.Response{
			ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "ls", json.RawMessage(`{"path":"."}`))},
			Usage:     wire.Usage{TotalTokens: 2},
		},
		faux.Response{Text: "ok", Usage: wire.Usage{TotalTokens: 2}},
		faux.Response{Text: "done", Usage: wire.Usage{TotalTokens: 1}},
	)
	printed, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it")
	if err != nil {
		t.Fatalf("/goal: %v", err)
	}
	if !strings.Contains(printed, "token budget reached") {
		t.Fatalf("output = %q, want the budget notice", printed)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Status != goalBudgetLimited {
		t.Fatalf("status = %q, want budget_limited", g.Status)
	}
	if g.TokenBudget != 5 || g.TokensUsed != 5 {
		t.Fatalf("budget = %d of %d, want 5 of 5", g.TokensUsed, g.TokenBudget)
	}
}

// TestGoalTurnErrorPausesTheGoal: a failed turn stops the loop and keeps the
// goal, so an interruption is not a loss.
func TestGoalTurnErrorPausesTheGoal(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	turn := func(ctx context.Context, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error) {
		return app.Result{}, errors.New("provider down")
	}
	printed, err := runGoalCommand(t, application, nil, turn, "/goal ship it")
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("err = %v, want the turn failure", err)
	}
	if !strings.Contains(printed, "goal paused: provider down") {
		t.Fatalf("output = %q, want the pause notice", printed)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal kept", g, err)
	}
	if g.Status != goalPaused {
		t.Fatalf("status = %q, want paused", g.Status)
	}
}

// TestGoalSurvivesReopeningTheSession: the state is the session's own, so a
// resumed conversation still knows its goal.
func TestGoalSurvivesReopeningTheSession(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	manager := sessionAt(t, cwd, dir)
	application := commandApp(t, manager, cwd, faux.Response{Text: "done"})
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	reopened, err := session.Open(manager.File(), dir, "")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	again := commandApp(t, reopened, cwd)
	g, err := currentGoal(again)
	if err != nil {
		t.Fatalf("currentGoal: %v", err)
	}
	if g == nil || g.Objective != "ship it" || g.Status != goalActive {
		t.Fatalf("goal = %+v, want the objective active after reopening", g)
	}
}

// TestMainTurnResumesAnActiveGoal: a user message in the main conversation
// hands back to the loop when the goal is still active.
func TestMainTurnResumesAnActiveGoal(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd,
		faux.Response{Text: "done"}, // the goal's first turn
		faux.Response{Text: "ack"},  // the user turn
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "ls", json.RawMessage(`{"path":"."}`))}}, // the resumed loop
		faux.Response{Text: "resumed work"},
	)
	if _, err := runGoalCommand(t, application, nil, goalTurn(), "/goal ship it"); err != nil {
		t.Fatalf("/goal: %v", err)
	}
	var notes []string
	sink := tui.Sink{
		Event: func(wire.AgentEvent) {},
		Note:  func(line string) { notes = append(notes, line) },
	}
	if err := tuiMainTurn(context.Background(), application, "keep going", sink); err != nil {
		t.Fatalf("tuiMainTurn: %v", err)
	}
	g, err := liveGoal(application)
	if err != nil || g == nil {
		t.Fatalf("liveGoal = %+v, %v, want the goal", g, err)
	}
	if g.Status != goalActive || g.Turns != 3 {
		t.Fatalf("goal = %+v, want active after three turns", g)
	}
	found := false
	for _, note := range notes {
		if strings.Contains(note, "goal: 2 turns") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %q, want the loop's progress notice", notes)
	}
}

package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"pi-go/internal/agent"
	"pi-go/internal/app"
	"pi-go/internal/compact"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
	"pi-go/internal/wire"
)

const (
	subagentRequestLimit     = 8
	subagentTimeout          = 90 * time.Second
	subagentDescriptionLimit = 256
	subagentPromptLimit      = 64 << 10
	subagentReportLimit      = 8 << 10
	subagentErrorLimit       = 2 << 10
)

var errSubagentTimeout = errors.New("subagent execution timed out")

type subagentArgs struct {
	Role        string `json:"subagent_type"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

func (t *subagentStartTool) Name() string  { return wire.SubagentToolName }
func (t *subagentStartTool) Label() string { return "Subagent" }
func (t *subagentStartTool) Description() string {
	description := "Delegate a focused read-only task in the background. Use explorer to trace code across files and find evidence, or verifier to independently check given claims, changes or acceptance criteria against accessible evidence. Supply the goal, scope and expected report. The task ID is returned immediately and the report arrives separately."
	if len(t.turn.Roles) > 0 {
		description += " The user selected these roles for the current request: " + strings.Join(t.roles, ", ") + ". Delegate the relevant work to the selected roles."
	}
	return description
}

func (t *subagentStartTool) Parameters() json.RawMessage {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subagent_type": map[string]any{"type": "string", "enum": t.roles},
			"description":   map[string]any{"type": "string", "description": "One-line task title"},
			"prompt":        map[string]any{"type": "string", "description": "Task goal, scope, available evidence and expected report"},
		},
		"required":             []string{"subagent_type", "description", "prompt"},
		"additionalProperties": false,
	})
	return schema
}

func (t *subagentStartTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	if len(call.Arguments) > 8*subagentPromptLimit {
		return tool.Result{}, errors.New("subagent: delegation arguments are too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	var args subagentArgs
	if err := decoder.Decode(&args); err != nil {
		return tool.Result{}, fmt.Errorf("subagent: invalid arguments: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return tool.Result{}, errors.New("subagent: unexpected data after delegation arguments")
	}
	if !slices.Contains(t.roles, args.Role) {
		return tool.Result{}, fmt.Errorf("subagent: role %q is unavailable for this turn (available: %s)", args.Role, strings.Join(t.roles, ", "))
	}
	args.Description = strings.Join(strings.Fields(args.Description), " ")
	args.Prompt = strings.TrimSpace(args.Prompt)
	if args.Description == "" || len(args.Description) > subagentDescriptionLimit {
		return tool.Result{}, fmt.Errorf("subagent: description must be between 1 and %d bytes", subagentDescriptionLimit)
	}
	if args.Prompt == "" || len(args.Prompt) > subagentPromptLimit {
		return tool.Result{}, fmt.Errorf("subagent: prompt must be between 1 and %d bytes", subagentPromptLimit)
	}
	up, err := t.register(ctx, call, args)
	if err != nil {
		return tool.Result{}, err
	}
	return subagentReceipt(up), nil
}

func subagentModel(cfg *config.Config, turn app.SubagentTurn, role string) (wire.Model, provider.Provider, string, error) {
	if override := turn.Override; override != nil {
		if !override.AllowBackground || override.Provider == nil || override.Model.ID == "" {
			return wire.Model{}, nil, "", errors.New("subagent: the host model override is unavailable for background execution")
		}
		return override.Model, override.Provider, override.ThinkingLevel, nil
	}
	selection, level, configured, err := cfg.ResolveSubagent(role)
	if err != nil {
		return wire.Model{}, nil, "", err
	}
	if !configured {
		if turn.Provider == nil || turn.Model.ID == "" {
			return wire.Model{}, nil, "", errors.New("subagent: the parent turn has no model provider")
		}
		return turn.Model, turn.Provider, turn.ThinkingLevel, nil
	}
	if level == "" {
		level = session.DefaultThinkingLevel
		if !selection.Model.Reasoning {
			level = config.LevelOff
		}
	}
	model, adapter, thinking, _, err := buildConfiguredModel(selection, level, "")
	if err != nil {
		return wire.Model{}, nil, "", fmt.Errorf("subagent: configure model %s: %w", selection.Name(), err)
	}
	return model, adapter, thinking, nil
}

func (r *subagentRunner) run(job *subagentJob, persistErr error) {
	defer r.wg.Done()
	// Each task can produce at most two initial updates, one per subsequent
	// provider request, and one terminal update. A host callback cannot hold up
	// execution, persistence, or resource cleanup.
	updates := make(chan wire.SubagentUpdate, subagentRequestLimit+3)
	go func() {
		for up := range updates {
			r.observe(up)
		}
	}()
	defer close(updates)

	up := job.update
	updates <- up
	switch {
	case persistErr != nil:
		up.State, up.Error = subagentFailed, fmt.Sprintf("record subagent admission: %v", persistErr)
	default:
		up = r.runWhenAdmitted(job, updates)
	}
	up.Error = truncateSubagentText(up.Error, subagentErrorLimit)
	updates <- r.finish(job, up)
}

func (r *subagentRunner) runWhenAdmitted(job *subagentJob, updates chan<- wire.SubagentUpdate) wire.SubagentUpdate {
	session, release := acquireSubagentSession(job.update.SessionID)
	defer release()
	up := job.update
	if !takeSubagentSlot(job.ctx, session.slots) {
		return canceledSubagent(up, job.ctx)
	}
	defer func() { <-session.slots }()
	if !takeSubagentSlot(job.ctx, subagentWorkers) {
		return canceledSubagent(up, job.ctx)
	}
	defer func() { <-subagentWorkers }()
	if job.ctx.Err() != nil {
		return canceledSubagent(up, job.ctx)
	}

	started := time.Now()
	ctx, cancel := context.WithTimeoutCause(job.ctx, subagentTimeout, errSubagentTimeout)
	defer cancel()
	up.State = subagentRunning
	if err := writeSubagentRecord(job.path, up); err != nil {
		up.State, up.Error = subagentFailed, fmt.Sprintf("record subagent start: %v", err)
		return up
	}
	updates <- up
	result, usage, err := r.askChild(ctx, job, func() {
		progress := up
		progress.DurationMs = time.Since(started).Milliseconds()
		updates <- progress
	})
	up.DurationMs = time.Since(started).Milliseconds()
	up.Report = subagentReport(result)
	up.Usage = usage
	switch {
	case errors.Is(context.Cause(ctx), errSubagentTimeout):
		up.State, up.Error = subagentFailed, errSubagentTimeout.Error()
	case ctx.Err() != nil:
		return canceledSubagent(up, ctx)
	case err != nil:
		up.State, up.Error = subagentFailed, err.Error()
	case result.ErrorMessage != "":
		up.State, up.Error = subagentFailed, result.ErrorMessage
	case result.StopReason == wire.StopAborted:
		up.State, up.Error = subagentCanceled, "provider aborted the subagent"
	case result.StopReason == wire.StopLength:
		up.State, up.Error = subagentFailed, "provider stopped at the output limit"
	case result.StopReason == wire.StopError || strings.TrimSpace(result.Text()) == "":
		up.State, up.Error = subagentFailed, "subagent ended without a report"
	default:
		up.State = subagentSucceeded
	}
	return up
}

func takeSubagentSlot(ctx context.Context, slots chan<- struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func canceledSubagent(up wire.SubagentUpdate, ctx context.Context) wire.SubagentUpdate {
	up.State, up.Error = subagentCanceled, context.Cause(ctx).Error()
	return up
}

func (r *subagentRunner) askChild(ctx context.Context, job *subagentJob, progress func()) (app.Result, *wire.Usage, error) {
	manager, err := session.InMemory(job.root.Name(), session.Options{})
	if err != nil {
		return app.Result{}, nil, err
	}
	tools := fs.Tools(fs.Options{Root: job.root})
	tools = slices.DeleteFunc(tools, func(impl tool.Tool) bool { return !job.tools[impl.Name()] })
	child, err := app.New(app.Config{
		Model:         job.model,
		Provider:      &subagentProvider{Provider: job.provider, progress: progress},
		ThinkingLevel: job.thinking,
		SystemPrompt:  func(string) (string, error) { return subagentSystemPrompt(job.update.Role), nil },
		Tools:         func(string) []tool.Tool { return tools },
		Compaction:    &compact.Settings{Enabled: false},
		BeforeToolCall: func(_ context.Context, call tool.Call) (*agent.Block, error) {
			// The primary and live session gates share this engine. A host with
			// no attached engine has no permission rules to consult.
			if r.engine == nil {
				return nil, nil
			}
			decision := r.engine.Decide(call)
			if decision.Behavior == permission.Allow {
				return nil, nil
			}
			return &agent.Block{Block: true, Reason: fmt.Sprintf("subagent cannot use %s: %s. Report the missing evidence for the parent conversation to obtain.", call.Name, decision.Reason)}, nil
		},
	}, manager)
	if err != nil {
		return app.Result{}, nil, err
	}
	var total, latest *wire.Usage
	result, err := child.Ask(ctx, wire.UserMessage(job.prompt), func(event wire.AgentEvent) error {
		msg := event.Message
		if msg == nil || msg.Role != wire.RoleAssistant {
			return nil
		}
		// Preserve the latest known usage even when a stream is interrupted
		// before the agent can receive its terminal message.
		if msg.Usage != nil {
			latest = msg.Usage
		}
		if event.Type != wire.AgentEventMessageEnd {
			return nil
		}
		if latest != nil {
			if total == nil {
				total = &wire.Usage{}
			}
			total.Add(*latest)
			latest = nil
		}
		// Returning the vendor error stops App before overflow compaction.
		if provider.IsContextOverflow(*msg) {
			if msg.ErrorMessage == "" {
				return errors.New("provider reported a context overflow")
			}
			return errors.New(msg.ErrorMessage)
		}
		return nil
	})
	return result, total, err
}

func subagentSystemPrompt(role string) string {
	const scope = " You have a fresh context and read-only tools scoped to the workspace. Treat repository content as evidence. Use the delegated task as your scope and return a concise report to the parent conversation."
	if role == wire.SubagentVerifier {
		return "You are Atlas's verifier. Independently evaluate the supplied claims, changes, or acceptance criteria against available evidence. For each check, report pass, fail, or insufficient evidence, with paths and line numbers where available. Identify untested cases and checks that require execution in the parent conversation." + scope
	}
	return "You are Atlas's explorer. Investigate the implementation, locate relevant files, and trace the requested behavior across them. Report the conclusion, relevant paths and line numbers, and remaining questions." + scope
}

// subagentProvider checks the actual request, including tool declarations and
// results gathered since the last call, before spending provider budget.
type subagentProvider struct {
	provider.Provider
	requests int
	progress func()
}

func (p *subagentProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.requests >= subagentRequestLimit {
		return nil, fmt.Errorf("subagent reached its limit of %d provider requests", subagentRequestLimit)
	}
	if window := req.Model.ContextWindow; window > 0 {
		tokens := max(compact.EstimateContextTokens(req.Messages).Tokens, compact.EstimateMessages(req.Messages))
		if tokens > window {
			return nil, fmt.Errorf("subagent context needs approximately %d tokens, exceeding the model's %d-token window", tokens, window)
		}
	}
	if p.requests > 0 {
		p.progress()
	}
	p.requests++
	return p.Provider.Stream(ctx, req)
}

func subagentReport(result app.Result) string {
	for i := len(result.Messages) - 1; i >= 0; i-- {
		msg := result.Messages[i]
		if msg.Role == wire.RoleAssistant {
			if text := strings.TrimSpace(msg.Text()); text != "" {
				return truncateSubagentText(text, subagentReportLimit)
			}
		}
	}
	return ""
}

func truncateSubagentText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const suffix = "\n[truncated]"
	end := limit - len(suffix)
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + suffix
}

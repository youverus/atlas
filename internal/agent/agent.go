// Package agent is the kernel: it drives model turns, executes tool calls, and
// emits lifecycle events. It knows nothing about sessions, transports, or the
// terminal.
package agent

import (
	"context"

	"pi-go/internal/provider"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// ToolExecution selects how tool calls from one assistant message run.
type ToolExecution string

const (
	// ToolExecutionSequential prepares, executes, and finalizes one call at a
	// time.
	ToolExecutionSequential ToolExecution = "sequential"
	// ToolExecutionParallel prepares calls in order, then runs them
	// concurrently. Default.
	ToolExecutionParallel ToolExecution = "parallel"
)

// Block is the result of a BeforeToolCall veto.
type Block struct {
	Block  bool
	Reason string
}

// Config is immutable after New. Optional callbacks may be nil.
type Config struct {
	Model    wire.Model
	Provider provider.Provider
	// ThinkingLevel is forwarded to the provider on every request. Empty means
	// the provider default.
	ThinkingLevel string
	SystemPrompt  string
	Tools         []tool.Tool

	ToolExecution ToolExecution
	MaxParallel   int

	// BeforeToolCall may veto a call. Arguments are raw; the tool decodes them.
	BeforeToolCall func(ctx context.Context, call tool.Call) (*Block, error)
	// AfterToolCall may rewrite the result before it is recorded.
	AfterToolCall func(ctx context.Context, call tool.Call, result *tool.Result)

	// GetSteeringMessages is polled between turns and injected before the next
	// request. It must not block.
	GetSteeringMessages func() []wire.Message
	// GetFollowUpMessages is polled only when the agent would otherwise stop.
	GetFollowUpMessages func() []wire.Message
}

// Agent owns one transcript and runs turns against one provider.
type Agent struct {
	cfg      Config
	registry *tool.Registry
	messages []wire.Message
}

// New creates an agent. Run is not safe to call concurrently.
func New(cfg Config) *Agent {
	if cfg.Provider == nil {
		panic("agent: Provider is required")
	}
	if cfg.ToolExecution == "" {
		cfg.ToolExecution = ToolExecutionParallel
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 8
	}
	return &Agent{cfg: cfg, registry: tool.NewRegistry(cfg.Tools...)}
}

// Registry exposes the tool registry so a caller can rebind it between runs.
//
// A tool set that changes while the session runs — an MCP server declaring a
// tool mid-conversation — is installed here, at the start of a turn, rather than
// by building a second agent: the registry is the only part of Config that is
// allowed to move after New.
func (a *Agent) Registry() *tool.Registry { return a.registry }

// Messages returns a copy of the transcript.
func (a *Agent) Messages() []wire.Message {
	return append([]wire.Message(nil), a.messages...)
}

// SetMessages replaces the transcript, for example when resuming a session.
func (a *Agent) SetMessages(messages []wire.Message) {
	a.messages = append([]wire.Message(nil), messages...)
}

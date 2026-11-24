package app

import (
	"context"
	"strings"
	"sync"

	"pi-go/internal/agent"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// sidePromptSuffix is appended to the main system prompt for a side
// conversation.
//
// The rules live in the prompt rather than in the tool layer because a side
// conversation uses the same tools as the main one. Teaching a tool to be
// read-only would mean the tool had to know which conversation called it, which
// is exactly the coupling the layering avoids.
const sidePromptSuffix = `
## Side conversation

This is a side conversation opened next to the main one. Its starting conversation history is a read-only snapshot: the main conversation keeps running in parallel, and nothing said here reaches it.

- Answer questions and explain. Do not change files or run commands that modify state unless asked explicitly.
- Do not continue or take over the main task.
- Everything here is discarded when the side conversation closes, so put anything worth keeping in the main conversation.
`

// Side is one temporary conversation branched off the main one.
//
// It inherits a read-only snapshot of the main history and shares the main
// model, thinking level, tools, and tool hooks. Its messages go into an
// in-memory session, so it never appears in session listings and leaves nothing
// behind when it closes.
type Side struct {
	mu            sync.Mutex
	manager       *session.Manager
	ag            *agent.Agent
	config        agent.Config
	promptSource  PromptSource
	tools         ToolSet
	parentSession tool.SessionContext
	steering      messageQueue
	followUps     messageQueue

	// messages mirrors the side session, for reads that may race with a turn.
	messages  []wire.Message
	forkEntry string
	busy      bool
	dirty     bool
	closed    bool
}

// BeginSide opens a side conversation and returns it.
//
// The snapshot is taken from the main session's current leaf, so it always ends
// at a committed message boundary and never captures a turn that is still
// running. Calling BeginSide again discards the previous side conversation: only
// one is kept alive at a time, which is what makes closing it a complete cleanup.
func (a *App) BeginSide() (*Side, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.side != nil {
		a.side.close()
		a.side = nil
	}

	snapshot, err := a.manager.BuildContext()
	if err != nil {
		return nil, err
	}

	manager, err := session.InMemory(a.manager.Cwd(), session.Options{})
	if err != nil {
		return nil, err
	}
	messages := make([]wire.Message, 0, len(snapshot.Messages))
	for _, message := range snapshot.Messages {
		// The side conversation carries its own system prompt, so the main one
		// is replaced instead of copied; keeping both would send the model two
		// different sets of instructions.
		if message.Role == wire.RoleSystem {
			continue
		}
		if _, err := manager.AppendMessage(message); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}

	side := &Side{
		manager:      manager,
		promptSource: a.promptSource,
		tools:        a.tools,
		parentSession: tool.SessionContext{
			ID:   a.manager.ID(),
			File: a.manager.File(),
			Cwd:  a.manager.Cwd(),
		},
		messages: append([]wire.Message(nil), messages...),
	}
	cfg := a.agentConfig(sideSystemPrompt(a.systemPrompt), &side.steering, &side.followUps)
	side.ag = agent.New(cfg)
	side.ag.SetMessages(messages)
	cfg.Tools = nil
	side.config = cfg
	if leaf := a.manager.LeafID(); leaf != nil {
		side.forkEntry = *leaf
	}
	a.side = side
	return side, nil
}

// EndSide closes the current side conversation, if any. It is safe to call when
// none is open.
func (a *App) EndSide() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.side == nil {
		return
	}
	a.side.close()
	a.side = nil
}

// Side returns the open side conversation, or nil. A side closed directly
// through Side.Close is dropped here rather than returned in a closed state.
func (a *App) Side() *Side {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.side != nil && a.side.isClosed() {
		a.side = nil
	}
	return a.side
}

func sideSystemPrompt(prompt string) string {
	base := strings.TrimSpace(prompt)
	if base == "" {
		return strings.TrimSpace(sidePromptSuffix)
	}
	return base + "\n" + sidePromptSuffix
}

// Ask runs one prompt on the side conversation.
//
// It is independent of the main conversation: the two run concurrently, and a
// slow side turn never blocks the main one.
func (s *Side) Ask(ctx context.Context, prompt wire.Message, emit func(wire.AgentEvent) error) (Result, error) {
	cwd := s.manager.Cwd()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Result{}, ErrSideClosed
	}
	if s.busy {
		s.mu.Unlock()
		return Result{}, ErrBusy
	}
	s.busy = true
	if s.dirty {
		cfg := s.config
		if s.tools != nil {
			cfg.Tools = s.tools(cwd)
		}
		s.rebuildLocked(cfg)
	}
	promptSource := s.promptSource
	tools := s.tools
	ag := s.ag
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.dirty && !s.closed {
			cfg := s.config
			if s.tools != nil {
				cfg.Tools = s.tools(cwd)
			}
			s.rebuildLocked(cfg)
		}
		s.busy = false
		s.mu.Unlock()
	}()

	if promptSource != nil {
		base, err := promptSource(cwd)
		if err != nil {
			return Result{}, err
		}
		next := sideSystemPrompt(base)
		s.mu.Lock()
		tools = s.tools
		if next != s.config.SystemPrompt {
			cfg := s.config
			cfg.SystemPrompt = next
			if tools != nil {
				cfg.Tools = tools(cwd)
			}
			s.rebuildLocked(cfg)
		}
		ag = s.ag
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		tools = s.tools
		ag = s.ag
		s.mu.Unlock()
	}
	if tools != nil {
		ag.Registry().Replace(tools(cwd)...)
	}
	return runTurn(tool.WithSession(ctx, s.parentSession), ag, []wire.Message{prompt}, s.persist, emit)
}

func (s *Side) updateRuntime(cfg agent.Config, tools ToolSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.tools = tools
	if s.busy {
		cfg.Tools = nil
		s.config = cfg
		s.dirty = true
		return
	}
	s.rebuildLocked(cfg)
}

func (s *Side) rebuildLocked(cfg agent.Config) {
	messages := s.ag.Messages()
	next := agent.New(cfg)
	next.SetMessages(messages)
	s.ag = next
	cfg.Tools = nil
	s.config = cfg
	s.dirty = false
}

// Steer queues a message for the running side turn, or for the next one.
func (s *Side) Steer(message wire.Message) { s.steering.enqueue(message) }

// FollowUp queues a message that is delivered when the side turn would
// otherwise stop.
func (s *Side) FollowUp(message wire.Message) { s.followUps.enqueue(message) }

// ClearQueue drops both side queues and returns what was waiting.
func (s *Side) ClearQueue() (steering, followUp []wire.Message) {
	return s.steering.clear(), s.followUps.clear()
}

// Close discards the conversation. Later prompts report ErrSideClosed; prefer
// App.EndSide, which also drops the side from the app.
func (s *Side) Close() { s.close() }

// Busy reports whether a turn is running on this side conversation.
func (s *Side) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Closed reports whether the conversation was discarded.
func (s *Side) Closed() bool { return s.isClosed() }

// Messages returns a snapshot of the side transcript, safe to call while a turn
// is running.
func (s *Side) Messages() []wire.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.Message(nil), s.messages...)
}

// Session exposes the in-memory session holding this conversation. It is never
// persisted.
func (s *Side) Session() *session.Manager { return s.manager }

// ForkEntry returns the main-session entry the snapshot was taken at, or "" when
// the main session had no entries yet. It is what a later "keep this side
// conversation" step would branch from.
func (s *Side) ForkEntry() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forkEntry
}

func (s *Side) persist(message wire.Message) error {
	if message.Role == wire.RoleSystem {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.manager.AppendMessage(message); err != nil {
		return err
	}
	s.messages = append(s.messages, message)
	return nil
}

func (s *Side) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *Side) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

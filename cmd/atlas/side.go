package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"pi-go/internal/agent"
	"pi-go/internal/app"
	"pi-go/internal/permission"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// The side conversation is the kernel's temporary branch with a terminal in
// front of it: /side switches the prompt into it, /parent switches back, and
// /side off throws it away. It never reaches disk, so nothing here needs a
// cleanup path on exit.

// sideReadOnly names the calls a side conversation may make without asking
// first. It is an allowlist, not a list of dangerous tools: a tool added later
// has to be classified on purpose instead of quietly escaping the gate.
var sideReadOnly = map[string]bool{
	"read": true,
	"ls":   true,
	"find": true,
	"grep": true,
	// The git tools belong here for the same reason the file readers do: they
	// only read, and the argv whitelist is what makes that true rather than
	// promised.
	"git_status": true,
	"git_diff":   true,
	"git_log":    true,
	"git_show":   true,
}

// sideCommands are the slash commands that still run while the prompt belongs
// to the side conversation. Everything else either changes the session the side
// branch was taken from (/new, /resume, /fork, /rename) or changes the model the
// side agent already copied at BeginSide (/model), and both would leave the
// terminal describing a conversation it is not talking to.
var sideCommands = map[string]bool{
	"/help":   true,
	"/side":   true,
	"/btw":    true,
	"/parent": true,
	// /mcp only describes the session: it reads the connections the side
	// conversation already borrowed and changes neither.
	"/mcp": true,
	// /skill reads a file and asks in the conversation the prompt belongs to.
	// It changes nothing about the session the snapshot came from, so the
	// request lands in the side conversation exactly as a typed one would.
	"/skill": true,
	// /plugin is a read of $ATLAS_HOME/plugins: it changes no session, and the
	// side conversation shares this machine with the main one.
	"/plugin": true,
	// /permission reads and writes the one engine both conversations gate on, so
	// a switch here is a switch for the main conversation too. That is what
	// makes it a command a side conversation can run: it changes what the next
	// call sees, not what the snapshot holds.
	permissionTrigger: true,
	"/exit":           true,
}

// The two things the model is told when a call did not run. Both are one line:
// the renderer prints a failure's reason under the call, so a paragraph would
// land in the user's scrollback on every refusal, and the rule it states is
// short enough to be worth reading anyway.
const (
	sideDeclined = "side conversation: the user declined this change. Reads run without asking; changes do not. Do not retry it here."
	sideNoReader = "side conversation: this change needs approval and there is no terminal to ask on, so only reads run here."
)

// sideState is what the terminal knows about the side conversation: which one
// exists, whether the prompt is typing into it, and how a call that would change
// something gets approved.
//
// Approval is decided from this state rather than from the caller. That is
// exact while the REPL runs one turn at a time — a turn typed into the side
// conversation is a side turn — and it is what lets the gate live on the app,
// which builds both agents from one config. A REPL that ever ran two turns at
// once would have to move the hook onto the side agent instead.
type sideState struct {
	mu     sync.Mutex
	side   *app.Side
	typing bool
	// home records that the front end draws a home screen for the side view.
	// When it does, a side conversation that has just been opened says what it
	// inherited on that page instead of in a notice: the notice would be the
	// second copy of one fact, one frame apart from the first.
	home bool
	// approve asks the user. Nil denies: a caller with no terminal cannot
	// approve anything, and guessing yes is the one answer that loses data.
	approve func(call tool.Call) bool
	// allowed is what the reader answered "for this session" to. It lives here
	// rather than with the gate because the gate is rebuilt for every turn and
	// this has to outlive them; it is still scoped to one session, which is what
	// makes it a small permission instead of a large one.
	allowed allowedTools
	// engine evaluates the permission ladder for both conversations. In the main
	// conversation it decides whether a call runs, asks, or is denied; in the
	// side conversation its deny rules still win before the read-only gate.
	engine *permission.Engine
}

func newSideState() *sideState {
	return newSideStateWith(permission.New(permission.Options{Mode: permission.ModeBypass}))
}

func newSideStateWith(engine *permission.Engine) *sideState {
	return &sideState{engine: engine}
}

// typingInSide reports whether the prompt belongs to the side conversation.
func (s *sideState) typingInSide() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.typing
}

// active returns the conversation the prompt is typing into, or nil when that
// is the main one. Reading both facts under one lock is what keeps a prompt from
// being sent to a conversation that was discarded between the two reads.
func (s *sideState) active() *app.Side {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.typing {
		return nil
	}
	return s.side
}

// conversation returns the open side conversation, or nil.
func (s *sideState) conversation() *app.Side {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.side
}

// enter moves the prompt into the side conversation.
func (s *sideState) enter(side *app.Side) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.side = side
	s.typing = true
}

// toMain moves the prompt back and keeps the conversation for a later /side.
func (s *sideState) toMain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typing = false
}

// discard drops the conversation, from either view.
func (s *sideState) discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.side = nil
	s.typing = false
}

// beforeToolCall is app.Config.BeforeToolCall: the gate that enforces the
// permission ladder in the main conversation and the read-only boundary in the
// side conversation.
func (s *sideState) beforeToolCall(ctx context.Context, call tool.Call) (*agent.Block, error) {
	s.mu.Lock()
	typing, approve, engine := s.typing, s.approve, s.engine
	s.mu.Unlock()

	if typing {
		if engine != nil {
			if decision := engine.Decide(call); decision.Behavior == permission.Deny {
				return &agent.Block{
					Block:  true,
					Reason: "permission denied: " + decision.Reason + ". Do not retry or work around this call.",
				}, nil
			}
		}
		if sideReadOnly[call.Name] {
			return nil, nil
		}
		if approve == nil {
			return &agent.Block{Block: true, Reason: sideNoReader}, nil
		}
		if !approve(call) {
			return &agent.Block{Block: true, Reason: sideDeclined}, nil
		}
		return nil, nil
	}

	if engine == nil {
		return nil, nil
	}
	if blocked, reason := engine.Gate(ctx, call, approve); blocked {
		return &agent.Block{Block: true, Reason: reason}, nil
	}
	return nil, nil
}

// enter names the conversation the next line is typed into. It is a notice like
// any other, so it lands in the transcript of the view being entered and reads
// back as the point where the prompt moved.
func enter(out io.Writer, name, note string) {
	fmt.Fprintf(out, "── %s ── %s\n", name, note)
}

// openSide switches the prompt into the side conversation, taking a snapshot of
// the main one when there is none to return to, and asks the question when one
// was typed on the same line.
func openSide(ctx context.Context, application *app.App, state *sideState, question string, out io.Writer, render *renderer) error {
	side := state.conversation()
	if side == nil || side.Closed() {
		opened, err := application.BeginSide()
		if err != nil {
			return err
		}
		state.enter(opened)
		render.setSide(true)
		// The count is what proves what was inherited: the snapshot ends at
		// the main session's last committed message, not at whatever was on
		// screen when the command ran. A front end with a page of its own for
		// the side view says it there instead — but only when the page will
		// actually be on screen, which is exactly when nothing was asked on
		// the same line: a question puts a block in the conversation before
		// the first frame, so there would be no page to read it on.
		if !state.home || strings.TrimSpace(question) != "" {
			enter(out, "side", fmt.Sprintf("%d messages inherited · %s", len(opened.Messages()), sideKeys))
		}
		side = opened
	} else {
		state.enter(side)
		render.setSide(true)
		// The notice is the marker of where the prompt moved, and a conversation
		// that is already on screen has no page left to say it on.
		enter(out, "side", fmt.Sprintf("%d messages · %s", len(side.Messages()), sideKeys))
	}
	// The question is not trimmed: leading spaces are indentation in a pasted
	// snippet, exactly as in the main conversation.
	if strings.TrimSpace(question) == "" {
		return nil
	}
	return askSideLine(ctx, side, question, render)
}

// sideKeys names the two ways out of the side view. It goes on every notice
// that moves the prompt, because the keys are the fast path and the commands are
// the ones a pipe or a person who forgot can still use. Ctrl+C is one of them
// and discarding is not: the key steps back one level, which leaves the
// conversation where it is.
const sideKeys = "Ctrl+C or Ctrl+O back to main, /side off discards it"

// toggleSide is Ctrl+O: it moves the prompt to the other conversation. It never
// opens one — a view switch must not take a snapshot as a side effect of a
// keystroke — so with nothing to move to it says what would open one, and says
// it as a notice rather than as an error, because nothing failed.
func toggleSide(state *sideState, out io.Writer, render *renderer) error {
	if state.typingInSide() {
		return leaveSide(state, out, render)
	}
	side := state.conversation()
	if side == nil || side.Closed() {
		fmt.Fprintln(out, "no side conversation; /side opens one")
		return nil
	}
	state.enter(side)
	render.setSide(true)
	enter(out, "side", fmt.Sprintf("%d messages · %s", len(side.Messages()), sideKeys))
	return nil
}

// leaveSide moves the prompt back to the main conversation and keeps the side
// one, which is what makes /parent reversible.
func leaveSide(state *sideState, out io.Writer, render *renderer) error {
	if !state.typingInSide() {
		return errors.New("/parent: already in the main conversation")
	}
	state.toMain()
	render.setSide(false)
	enter(out, "main", "the side conversation is kept; Ctrl+O or /side returns to it")
	return nil
}

// discardSide closes the side conversation and drops the prompt back to the
// main one when it was in the side view. It is the only way to lose a side
// conversation — Ctrl+C and Ctrl+O both step back and keep it — so the title
// says where the prompt landed.
func discardSide(application *app.App, state *sideState, out io.Writer, render *renderer) error {
	if state.conversation() == nil {
		return errors.New("/side off: no side conversation is open")
	}
	inSide := state.typingInSide()
	application.EndSide()
	state.discard()
	render.setSide(false)
	if inSide {
		enter(out, "main", "side closed")
		return nil
	}
	fmt.Fprintln(out, "side discarded")
	return nil
}

// askSide runs one turn on the side conversation with a message already built.
// Sharing runTurn with the main conversation is what keeps Ctrl-C, the token
// summary and the failure report identical in both.
func askSide(ctx context.Context, side *app.Side, message wire.Message, render *renderer) error {
	return runTurn(ctx, render, func(turnCtx context.Context, emit func(wire.AgentEvent) error) (app.Result, error) {
		return side.Ask(turnCtx, message, emit)
	})
}

// askSideLine is the side conversation's askLine: a question the reader typed, so
// an "@path" in it becomes content. It resolves against the side conversation's
// own directory, which is the one its tools point at.
func askSideLine(ctx context.Context, side *app.Side, line string, render *renderer) error {
	message, err := attachMessage(line, side.Session().Cwd())
	if err != nil {
		return err
	}
	return askSide(ctx, side, message, render)
}

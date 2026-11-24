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
	"pi-go/internal/assembly"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// The side conversation is the kernel's temporary branch with a terminal in
// front of it: /side switches the prompt into it, /parent switches back, and
// /side off throws it away. It never reaches disk, so nothing here needs a
// cleanup path on exit.

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
	"/mcp":     true,
	"/browser": true,
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

// sideState is what the terminal knows about the side conversation: which one
// exists, whether the prompt is typing into it, and which gate decides what a
// call may do.
//
// The gate itself is the assembly's, because a window asks the same question
// through a blocking POST that this terminal asks through a keypress. What the
// terminal adds is the one fact the gate cannot know: whether the next turn is
// typed into the main conversation or into a side one. That is why
// beforeToolCall is here and the ladder is not.
type sideState struct {
	mu     sync.Mutex
	side   *app.Side
	typing bool
	// home records that the front end draws a home screen for the side view.
	// When it does, a side conversation that has just been opened says what it
	// inherited on that page instead of in a notice: the notice would be the
	// second copy of one fact, one frame apart from the first.
	home bool
	// gate is the approval path both conversations pass. Nil means nothing is
	// gated, which is what a test that never configured permissions gets.
	gate *assembly.Gate
	// ask is the question the gate puts to the reader, and note reports a
	// standing yes. Both are installed per turn because they close over the
	// turn's sink.
	ask  func(assembly.Ask) assembly.Answer
	note func(string)
	// approve decides by rule instead of by question: a caller that already
	// knows the answer says so here rather than growing a modal. It is how a
	// test describes a posture, and it takes precedence over ask.
	approve func(call tool.Call) bool
}

// newSideState is a state whose gate has no permission engine behind it. The
// gate still enforces the read-only boundary of a side conversation and still
// asks before a write; what it lacks is the mode switch, which is why the demo
// and every test that does not build an engine use this.
func newSideState() *sideState {
	return newSideStateWith(assembly.NewGate(nil))
}

func newSideStateWith(gate *assembly.Gate) *sideState {
	return &sideState{gate: gate}
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

// setAsk installs the question the gate puts to the reader. The terminal owns it
// because the question closes over the turn's sink, and the gate outlives the
// turn.
func (s *sideState) setAsk(ask func(assembly.Ask) assembly.Answer, note func(string)) {
	s.mu.Lock()
	s.ask, s.note = ask, note
	s.mu.Unlock()
	s.sync()
}

// sync hands the current question to the gate. A caller that decides by rule
// wins over one that asks: there is only ever one of them, and the rule is the
// more specific statement of intent.
func (s *sideState) sync() {
	if s.gate == nil {
		return
	}
	s.mu.Lock()
	approve, ask, note := s.approve, s.ask, s.note
	s.mu.Unlock()
	switch {
	case approve != nil:
		s.gate.SetAsk(func(req assembly.Ask) assembly.Answer {
			return assembly.Answer{Allowed: approve(tool.Call{Name: req.Tool})}
		}, note)
	case ask != nil:
		s.gate.SetAsk(ask, note)
	}
}

// beforeToolCall is app.Config.BeforeToolCall: the gate that enforces the
// permission ladder in the main conversation and the read-only boundary in the
// side conversation.
//
// It reads the typing flag first because that is the half of the decision only
// this package knows, and hands the rest to the gate the kernel built. A REPL
// that ever ran two turns at once would have to move both onto the side agent
// instead.
func (s *sideState) beforeToolCall(ctx context.Context, call tool.Call) (*agent.Block, error) {
	if s == nil || s.gate == nil {
		return nil, nil
	}
	s.sync()
	s.gate.SetTyping(s.typingInSide())
	return s.gate.BeforeToolCall(ctx, call)
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
func openSide(ctx context.Context, application *app.App, state *sideState, question string, out io.Writer, render *renderer, images ...string) error {
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
	message, err := interactiveMessage(question, images)
	if err != nil {
		return err
	}
	return askSide(ctx, side, message, render)
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

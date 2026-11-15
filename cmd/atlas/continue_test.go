package main

// /continue: the command that resumes the turn a reader stopped.
//
// It is the only command whose subject is not in the command line: everything it
// needs is the shape of the conversation's last turn, which the session already
// recorded.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

// stoppedProvider speaks part of an answer and then waits for the caller to stop
// it, which is what a stream looks like when a reader presses escape. Later
// requests answer in full, so one test can run both the interruption and the
// continuation.
type stoppedProvider struct {
	said  string
	next  []string
	mu    sync.Mutex
	calls [][]wire.Message
}

func (p *stoppedProvider) API() string { return "stopped" }

func (p *stoppedProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req.Messages)
	first := len(p.calls) == 1
	var answer string
	if !first && len(p.next) > 0 {
		answer = p.next[0]
		p.next = p.next[1:]
	}
	p.mu.Unlock()

	frame := provider.NewFrame(req.Model)
	events := []wire.AssistantEvent{frame.Start()}
	if first {
		events = append(events, frame.TextStart(0))
		for _, chunk := range strings.SplitAfter(p.said, " ") {
			if chunk != "" {
				events = append(events, frame.TextDelta(0, chunk))
			}
		}
		// No TextEnd and no Done: the block never closed, which is the shape the
		// loop has to keep as a partial.
		return &waitingStream{ctx: ctx, events: events}, nil
	}
	events = append(events,
		frame.TextStart(0), frame.TextDelta(0, answer), frame.TextEnd(0), frame.Done(wire.StopStop))
	return &waitingStream{ctx: ctx, events: events}, nil
}

// Requests returns the message lists this provider was asked to answer, in
// order.
func (p *stoppedProvider) Requests() [][]wire.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]wire.Message(nil), p.calls...)
}

type waitingStream struct {
	ctx    context.Context
	events []wire.AssistantEvent
	next   int
}

func (s *waitingStream) Recv() (wire.AssistantEvent, error) {
	if s.next < len(s.events) {
		event := s.events[s.next]
		s.next++
		return event, nil
	}
	<-s.ctx.Done()
	return wire.AssistantEvent{}, s.ctx.Err()
}

func (s *waitingStream) Close() error { return nil }

// interruptedApp builds a conversation whose last turn was stopped mid-answer,
// which is what a reader leaves behind by pressing escape.
func interruptedApp(t *testing.T, cwd string, prov provider.Provider, said string) *app.App {
	t.Helper()
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     prov,
		SystemPrompt: promptBuilder{noSkills: true}.source().prompt,
	}, memoryAt(t, cwd))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The cancel lands after the model has said something, which is the moment
	// the reader would have pressed escape.
	emit := func(event wire.AgentEvent) error {
		if event.Type == wire.AgentEventMessageUpdate {
			cancel()
		}
		return nil
	}
	if _, err := application.Ask(ctx, wire.UserMessage("tell me a story"), emit); err == nil {
		t.Fatal("the turn was not interrupted")
	}
	if got := application.Continuation(); !got.Resumable() {
		t.Fatalf("the interruption is not resumable: %+v", got)
	}
	_ = said
	return application
}

// TestContinueResumesTheInterruptedTurn is the acceptance test for the command:
// after a turn is stopped, /continue sends one more request and says what it
// picked up.
func TestContinueResumesTheInterruptedTurn(t *testing.T) {
	cwd := t.TempDir()
	prov := &stoppedProvider{said: "once upon a time", next: []string{"and they lived"}}
	application := interruptedApp(t, cwd, prov, "once upon a time")

	recorder := &homeSink{}
	line := "/continue"
	if err := runTUICommand(context.Background(), application, nil, mcpState{}, models{},
		newSessionPicker(application, ""), emptyPrompts(), nil, line, recorder.sink()); err != nil {
		t.Fatalf("%s: %v", line, err)
	}

	// The line names the break, because a continued answer reopens a block the
	// reader has to be able to place.
	if len(recorder.notes) != 1 || !strings.Contains(recorder.notes[0], "continuing after") {
		t.Fatalf("%s said %q", line, recorder.notes)
	}
	if !strings.Contains(recorder.notes[0], "16 characters") {
		t.Fatalf("%s counted %q", line, recorder.notes[0])
	}

	requests := prov.Requests()
	if len(requests) != 2 {
		t.Fatalf("%s sent %d requests, want the turn and the continuation", line, len(requests))
	}
	sent := requests[1]
	// The question, the sentence that was kept, and the turn saying to go on.
	if len(sent) != 4 {
		t.Fatalf("the continuation carried %d messages: %+v", len(sent), sent)
	}
	if sent[2].Text() != "once upon a time" {
		t.Fatalf("the continuation lost the interruption: %+v", sent)
	}
	if last := sent[len(sent)-1]; last.Role != wire.RoleUser || last.Text() == "" {
		t.Fatalf("the continuation sent no prompt: %+v", sent)
	}
}

// TestContinueRefusesAFinishedTurn: the command has to answer a reader who typed
// it after a turn that ended, and refuse without spending a request.
func TestContinueRefusesAFinishedTurn(t *testing.T) {
	cwd := t.TempDir()
	manager := memoryAt(t, cwd)
	application := commandApp(t, manager, cwd, faux.Response{Text: "hello"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), noEvents); err != nil {
		t.Fatalf("ask: %v", err)
	}

	recorder := &homeSink{}
	err := runTUICommand(context.Background(), application, nil, mcpState{}, models{},
		newSessionPicker(application, ""), emptyPrompts(), nil, "/continue", recorder.sink())
	if err == nil {
		t.Fatal("/continue accepted a turn that finished")
	}
	if !strings.Contains(err.Error(), "finished") {
		t.Fatalf("/continue said %q", err)
	}
	if len(recorder.notes) != 0 {
		t.Fatalf("/continue said %q before refusing", recorder.notes)
	}
}

// TestContinueRefusesAnArgument: what to continue is the last turn, and a
// session has one of those, so an argument has nothing to choose between.
func TestContinueRefusesAnArgument(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "hello"})
	out, err := runCommand(t, application, "/continue that one")
	if err == nil {
		t.Fatal("/continue accepted an argument")
	}
	if !strings.Contains(err.Error(), "takes no argument") {
		t.Fatalf("/continue said %q", err)
	}
	if out != "" {
		t.Fatalf("/continue wrote %q", out)
	}
}

// TestContinueIsNotASideConversationCommand: the command continues the main
// conversation, so in a side one it would resume a turn the reader is not in.
func TestContinueIsNotASideConversationCommand(t *testing.T) {
	cwd := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, cwd, faux.Response{Text: "hello"})
	opened := &bytes.Buffer{}
	if err := openSide(context.Background(), application, state, "", opened, newRenderer(opened, opened, false)); err != nil {
		t.Fatalf("openSide: %v", err)
	}

	out, err := runSideCommand(t, application, state, "/continue")
	if err == nil {
		t.Fatal("/continue ran in a side conversation")
	}
	if !strings.Contains(err.Error(), "does not run in a side conversation") {
		t.Fatalf("/continue said %q", err)
	}
	if out != "" {
		t.Fatalf("/continue wrote %q", out)
	}
}

// TestContinueIsInTheHelpTable: /help is the one place a reader looks for what
// exists, so a command missing from the table cannot be found.
func TestContinueIsInTheHelpTable(t *testing.T) {
	if !strings.Contains(helpText(), "/continue") {
		t.Fatal("/help does not mention /continue")
	}
	// And it is not offered where it cannot run.
	for _, pick := range suggestionsFor("/con", nil, false) {
		if pick.Name != "/continue" {
			continue
		}
		if pick.About == "" {
			t.Fatal("/continue has no description in the palette")
		}
	}
	for _, pick := range suggestionsFor("/", nil, true) {
		if pick.Name == "/continue" {
			t.Fatal("/continue is offered in an archived session")
		}
	}
}

// TestContinueRefusesAnArchivedSession: an archived file is a record, and a
// continuation appends to it. The command is refused before the turn runner is
// ever reached, which is why this passes one.
func TestContinueRefusesAnArchivedSession(t *testing.T) {
	cwd := t.TempDir()
	manager := sessionAt(t, cwd, t.TempDir())
	prov := &stoppedProvider{said: "once upon a time", next: []string{"and they lived"}}
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     prov,
		SystemPrompt: promptBuilder{noSkills: true}.source().prompt,
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if err := application.SetName("alpha"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	// An interrupted turn, then the file put away: the state the command has to
	// refuse is an archive that would otherwise be resumable.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupt := func(event wire.AgentEvent) error {
		if event.Type == wire.AgentEventMessageUpdate {
			cancel()
		}
		return nil
	}
	if _, err := application.Ask(ctx, wire.UserMessage("tell me a story"), interrupt); err == nil {
		t.Fatal("the turn was not interrupted")
	}
	id := application.Session().ID()
	if _, err := runCommand(t, application, "/archive"); err != nil {
		t.Fatalf("/archive: %v", err)
	}
	if _, err := runCommand(t, application, "/recover "+id[:8]); err != nil {
		t.Fatalf("/recover: %v", err)
	}
	if !application.Archived() {
		t.Fatal("/recover did not open the archived session")
	}

	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	turn := func(turnCtx context.Context, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error) {
		t.Fatal("/continue reached the turn runner in an archived session")
		return app.Result{}, nil
	}
	_, err = replCommand(context.Background(), application, nil, "/continue", models{},
		newSessionPicker(application, ""), nil, emptyPrompts(), out, render, mcpState{}, nil, turn)
	if !errors.Is(err, app.ErrArchived) {
		t.Fatalf("/continue = %v, want ErrArchived", err)
	}
}

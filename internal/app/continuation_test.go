package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// abortedText is the assistant message an interrupted answer leaves behind: the
// sentence that had arrived, with the stop reason saying it did not finish.
func abortedText(text string) wire.Message {
	message := wire.AssistantMessage(wire.TextContent(text))
	message.StopReason = wire.StopAborted
	return message
}

// failedText is the same message for a provider failure rather than a stop.
func failedText(text string) wire.Message {
	message := wire.AssistantMessage(wire.TextContent(text))
	message.StopReason = wire.StopError
	return message
}

// interruptedResult is a tool result the reader stopped, carrying what it had
// produced before that.
func interruptedResult(text string) wire.Message {
	message := wire.ToolResultMessage("call_1", "write", true, wire.TextContent("Operation aborted"), wire.TextContent(text))
	message.Interrupted = true
	return message
}

func TestContinuationOf(t *testing.T) {
	cases := []struct {
		name      string
		messages  []wire.Message
		want      ContinuationKind
		chars     int
		calls     int
		resumable bool
	}{
		{
			name:     "no messages",
			messages: nil,
			want:     ContinuationNone,
		},
		{
			name:     "an answered turn",
			messages: []wire.Message{wire.UserMessage("hi"), wire.AssistantMessage(wire.TextContent("hello"))},
			want:     ContinuationNone,
		},
		{
			name:      "an answer that was cut off",
			messages:  []wire.Message{wire.UserMessage("hi"), abortedText("half an ans")},
			want:      ContinuationAnswer,
			chars:     11,
			resumable: true,
		},
		{
			// Runes, not bytes: the count is read by a person looking at
			// characters.
			name:      "a cut-off answer keeps counting runes",
			messages:  []wire.Message{wire.UserMessage("hi"), abortedText("héllo")},
			want:      ContinuationAnswer,
			chars:     5,
			resumable: true,
		},
		{
			name:      "a prompt that never got an answer",
			messages:  []wire.Message{wire.AssistantMessage(wire.TextContent("hello")), wire.UserMessage("hi")},
			want:      ContinuationPrompt,
			resumable: true,
		},
		{
			name:      "tool results that were cut off",
			messages:  []wire.Message{wire.UserMessage("hi"), abortedText(""), interruptedResult("3 lines")},
			want:      ContinuationTools,
			calls:     1,
			resumable: true,
		},
		{
			name: "a batch of interrupted results",
			messages: []wire.Message{
				wire.UserMessage("hi"),
				abortedText(""),
				interruptedResult("a"),
				interruptedResult("b"),
			},
			want:      ContinuationTools,
			calls:     2,
			resumable: true,
		},
		{
			// A batch that finished has results too, and a terminating one ends
			// the turn there: the run is over, not cut off.
			name: "results that all finished",
			messages: []wire.Message{
				wire.UserMessage("hi"),
				wire.ToolResultMessage("call_1", "echo", false, wire.TextContent("done")),
			},
			want: ContinuationNone,
		},
		{
			// Only the tail of the transcript is the question: an interrupted
			// batch that was answered two turns ago is history.
			name: "an interrupted batch that was answered",
			messages: []wire.Message{
				wire.UserMessage("hi"),
				abortedText(""),
				interruptedResult("a"),
				wire.AssistantMessage(wire.TextContent("continued")),
				wire.UserMessage("next"),
				wire.AssistantMessage(wire.TextContent("answer")),
			},
			want: ContinuationNone,
		},
		{
			// The provider's own failure, not the reader's: the turn stopped
			// without finishing either, and continuing is the same command.
			name:      "a turn that failed",
			messages:  []wire.Message{wire.UserMessage("hi"), failedText("half an ans")},
			want:      ContinuationAnswer,
			chars:     11,
			resumable: true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := continuationOf(test.messages)
			if got.Kind != test.want {
				t.Fatalf("kind: got %q want %q (reason %q)", got.Kind, test.want, got.Reason)
			}
			if got.Resumable() != test.resumable {
				t.Fatalf("resumable: got %v want %v", got.Resumable(), test.resumable)
			}
			if got.Chars != test.chars {
				t.Fatalf("chars: got %d want %d", got.Chars, test.chars)
			}
			if got.Calls != test.calls {
				t.Fatalf("calls: got %d want %d", got.Calls, test.calls)
			}
			if !got.Resumable() && got.Reason == "" {
				t.Fatal("a refused continuation has to say why")
			}
		})
	}
}

// stopper cancels the turn from inside the event stream, which is the
// deterministic way to interrupt one: the cancel lands between two events of the
// turn being stopped, so nothing races a timer.
type stopper struct {
	eventType string
	once      sync.Once
	cancel    func()
}

func (s *stopper) stop(event wire.AgentEvent) error {
	if event.Type == s.eventType {
		s.once.Do(s.cancel)
	}
	return nil
}

// interruptedProvider speaks part of an answer and then waits for the caller to
// give up, which is what a stream looks like when a reader stops it. Every later
// request is delegated, so one test can run the interruption and the
// continuation that follows it.
type interruptedProvider struct {
	said  string
	rest  provider.Provider
	mu    sync.Mutex
	calls []provider.Request
}

func (p *interruptedProvider) API() string { return "interrupted" }

func (p *interruptedProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	first := len(p.calls) == 1
	p.mu.Unlock()
	if !first {
		return p.rest.Stream(ctx, req)
	}

	frame := provider.NewFrame(req.Model)
	events := []wire.AssistantEvent{frame.Start(), frame.TextStart(0)}
	for _, chunk := range strings.SplitAfter(p.said, " ") {
		if chunk != "" {
			events = append(events, frame.TextDelta(0, chunk))
		}
	}
	// No TextEnd and no Done: an answer that was cut off never closes its block,
	// which is why the loop has to keep the partial instead.
	return &spokenStream{ctx: ctx, events: events, hang: true}, nil
}

// Calls returns the requests this provider saw before it delegated.
func (p *interruptedProvider) Calls() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.calls...)
}

// spokenStream replays a script; with hang set it then waits for the caller
// rather than finishing.
type spokenStream struct {
	ctx    context.Context
	events []wire.AssistantEvent
	next   int
	hang   bool
}

func (s *spokenStream) Recv() (wire.AssistantEvent, error) {
	if s.next < len(s.events) {
		event := s.events[s.next]
		s.next++
		return event, nil
	}
	if !s.hang {
		return wire.AssistantEvent{}, io.EOF
	}
	<-s.ctx.Done()
	return wire.AssistantEvent{}, s.ctx.Err()
}

func (s *spokenStream) Close() error { return nil }

// interruptedAnswer runs one turn on the main conversation, stops it as soon as
// the model has said something, and returns the app that survived it.
func interruptedAnswer(t *testing.T, said string, rest provider.Provider) (*App, *interruptedProvider) {
	t.Helper()
	prov := &interruptedProvider{said: said, rest: rest}
	application := newTestApp(t, newSession(t), prov)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &stopper{eventType: wire.AgentEventMessageUpdate, cancel: cancel}
	if _, err := application.Ask(ctx, wire.UserMessage("tell me a story"), stop.stop); err == nil {
		t.Fatal("the turn was not interrupted")
	}
	return application, prov
}

func TestContinueAfterAnInterruptedAnswer(t *testing.T) {
	rest := faux.New(faux.Response{Text: "and they lived"})
	application, prov := interruptedAnswer(t, "once upon a time", rest)

	// What the reader saw is what the session kept: the sentence that arrived,
	// not an empty message where it used to be.
	kept := application.Messages()
	last := kept[len(kept)-1]
	if last.Role != wire.RoleAssistant || last.StopReason != wire.StopAborted {
		t.Fatalf("tail of the interrupted session: %+v", last)
	}
	if last.Text() != "once upon a time" {
		t.Fatalf("the interrupted answer was not kept: %q", last.Text())
	}

	continuation := application.Continuation()
	if continuation.Kind != ContinuationAnswer || continuation.Chars != len("once upon a time") {
		t.Fatalf("continuation: %+v", continuation)
	}

	result, err := application.Continue(context.Background(), nil)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if result.Text() != "and they lived" {
		t.Fatalf("answer: got %q", result.Text())
	}

	sent := rest.Calls()[0].Messages
	// The system prompt, the question, the sentence that was kept, and the turn
	// saying to go on. Asking the question again would be a re-run.
	if len(sent) != 4 {
		t.Fatalf("request carried %d messages: %+v", len(sent), sent)
	}
	if sent[1].Text() != "tell me a story" || sent[2].Text() != "once upon a time" {
		t.Fatalf("request did not keep the interruption: %+v", sent)
	}
	if sent[3].Role != wire.RoleUser || sent[3].Text() != continuePrompt {
		t.Fatalf("request did not ask for the rest: %+v", sent)
	}
	// The prompt is in the session as well, so replaying the file rebuilds the
	// request the model just answered.
	tail := application.Messages()
	if tail[len(tail)-2].Text() != continuePrompt {
		t.Fatalf("the continuation prompt was not recorded: %+v", tail)
	}
	if first := prov.Calls()[0].Messages; first[len(first)-1].Text() != "tell me a story" {
		t.Fatalf("the interrupted request did not carry the question: %+v", first)
	}
}

// halfTool reports what it has done and then waits, which is a file written
// halfway when the reader stops the turn.
type halfTool struct {
	name     string
	produced string
}

func (h *halfTool) Name() string        { return h.name }
func (h *halfTool) Label() string       { return h.name }
func (h *halfTool) Description() string { return "test tool " + h.name }

func (h *halfTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (h *halfTool) Execute(ctx context.Context, _ tool.Call, progress func(tool.Partial)) (tool.Result, error) {
	progress(tool.Partial{Content: []wire.Content{wire.TextContent(h.produced)}})
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func TestContinueAfterInterruptedTools(t *testing.T) {
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "write", json.RawMessage(`{}`))}},
		faux.Response{Text: "the file is half written"},
	)
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Tools = StaticTools([]tool.Tool{&halfTool{name: "write", produced: "wrote 3 of 10 lines"}})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &stopper{eventType: wire.AgentEventToolExecUpdate, cancel: cancel}
	if _, err := application.Ask(ctx, wire.UserMessage("write the file"), stop.stop); err == nil {
		t.Fatal("the turn was not interrupted")
	}

	// The result says which call was stopped and what it had done, because
	// "Operation aborted" alone cannot tell a half-written file from an
	// untouched one.
	messages := application.Messages()
	var result *wire.Message
	for i := range messages {
		if messages[i].Role == wire.RoleToolResult {
			result = &messages[i]
		}
	}
	if result == nil {
		t.Fatal("the interrupted turn recorded no tool result")
	}
	if !result.Interrupted {
		t.Fatalf("the result does not say it was interrupted: %+v", result)
	}
	if !strings.Contains(result.Text(), "wrote 3 of 10 lines") {
		t.Fatalf("the result dropped what the tool had produced: %q", result.Text())
	}

	continuation := application.Continuation()
	if continuation.Kind != ContinuationTools || continuation.Calls != 1 {
		t.Fatalf("continuation: %+v", continuation)
	}

	resumed, err := application.Continue(context.Background(), nil)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if resumed.Text() != "the file is half written" {
		t.Fatalf("answer: got %q", resumed.Text())
	}

	// A tool result already ends where the answer belongs, so the continuation
	// sends no prompt: nothing was typed to resume, and inventing a sentence
	// would put words in the reader's mouth. The question stays the only user
	// turn in the request.
	sent := prov.Calls()[1].Messages
	var asked []wire.Message
	for _, message := range sent {
		if message.Role == wire.RoleUser {
			asked = append(asked, message)
		}
	}
	if len(asked) != 1 || asked[0].Text() != "write the file" {
		t.Fatalf("a continued tool batch added a user turn: %+v", sent)
	}
	var carried *wire.Message
	for i := range sent {
		if sent[i].Role == wire.RoleToolResult {
			carried = &sent[i]
		}
	}
	if carried == nil || !carried.Interrupted {
		t.Fatalf("request did not carry the interrupted result: %+v", sent)
	}
}

// A prompt whose request never returned leaves the session ending with the
// question. Seeding the session file is how a process that died mid-request
// leaves it behind.
func TestContinueAfterAnUnansweredPrompt(t *testing.T) {
	manager := newSession(t)
	if _, err := manager.AppendMessage(wire.UserMessage("are you there")); err != nil {
		t.Fatalf("append: %v", err)
	}
	prov := faux.New(faux.Response{Text: "yes"})
	application := newTestApp(t, manager, prov)

	if got := application.Continuation(); got.Kind != ContinuationPrompt {
		t.Fatalf("continuation: %+v", got)
	}
	if _, err := application.Continue(context.Background(), nil); err != nil {
		t.Fatalf("continue: %v", err)
	}
	sent := prov.Calls()[0].Messages
	if len(sent) != 2 || sent[1].Text() != "are you there" {
		t.Fatalf("the request did not resend the question once: %+v", sent)
	}
}

func TestContinueRefusesAFinishedTurn(t *testing.T) {
	prov := faux.New(faux.Response{Text: "hello"})
	application := newTestApp(t, newSession(t), prov)

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := application.Continuation(); got.Resumable() || got.Reason == "" {
		t.Fatalf("continuation: %+v", got)
	}
	if _, err := application.Continue(context.Background(), nil); !errors.Is(err, ErrNothingToContinue) {
		t.Fatalf("continue: got %v want ErrNothingToContinue", err)
	}
	if got := len(prov.Calls()); got != 1 {
		t.Fatalf("a refused continuation still sent a request: %d calls", got)
	}
}

// A restart rebuilds the app from the session file. The continuation has to
// survive that: the interruption and the process that saw it are both gone, and
// what is left is the file.
func TestContinuationSurvivesARestart(t *testing.T) {
	application, _ := interruptedAnswer(t, "half an ans", faux.New(faux.Response{Text: "the rest"}))
	file := application.Session().File()
	if file == "" {
		t.Fatal("the interrupted session was not persisted")
	}

	reopened, err := session.Open(file, "", "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	prov := faux.New(faux.Response{Text: "the rest of it"})
	restarted := newTestApp(t, reopened, prov)

	continuation := restarted.Continuation()
	if continuation.Kind != ContinuationAnswer || continuation.Chars != len("half an ans") {
		t.Fatalf("continuation after restart: %+v", continuation)
	}
	result, err := restarted.Continue(context.Background(), nil)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if result.Text() != "the rest of it" {
		t.Fatalf("answer: got %q", result.Text())
	}
	sent := prov.Calls()[0].Messages
	if last := sent[len(sent)-1]; last.Text() != continuePrompt {
		t.Fatalf("the restarted continuation sent %+v", sent)
	}
	if sent[len(sent)-2].Text() != "half an ans" {
		t.Fatalf("the restarted continuation lost the interruption: %+v", sent)
	}
}

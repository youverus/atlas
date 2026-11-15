package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"pi-go/internal/agent"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

var testModel = wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"}

// funcTool is a configurable test tool that records its calls.
type funcTool struct {
	name      string
	delay     time.Duration
	terminate bool
	err       error

	mu    sync.Mutex
	calls []tool.Call
}

func (t *funcTool) Name() string  { return t.name }
func (t *funcTool) Label() string { return t.name }
func (t *funcTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *funcTool) Description() string { return "test tool " + t.name }

func (t *funcTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	if t.delay > 0 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}
	t.mu.Lock()
	t.calls = append(t.calls, call)
	t.mu.Unlock()
	if t.err != nil {
		return tool.Result{}, t.err
	}
	return tool.Result{
		Content:   []wire.Content{wire.TextContent(t.name + " result")},
		Terminate: t.terminate,
	}, nil
}

func (t *funcTool) Calls() []tool.Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]tool.Call(nil), t.calls...)
}

func runAgent(t *testing.T, cfg agent.Config, prompts ...wire.Message) ([]wire.Message, []wire.AgentEvent, error) {
	t.Helper()
	a := agent.New(cfg)
	var events []wire.AgentEvent
	produced, err := a.Run(context.Background(), prompts, func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	return produced, events, err
}

func countEvents(events []wire.AgentEvent, eventType string) int {
	total := 0
	for _, event := range events {
		if event.Type == eventType {
			total++
		}
	}
	return total
}

func TestTextRun(t *testing.T) {
	p := faux.New(faux.Response{Text: "hello"})
	produced, events, err := runAgent(t, agent.Config{
		Model:        testModel,
		Provider:     p,
		SystemPrompt: "you are a test",
	}, wire.UserMessage("hi"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(produced) != 2 {
		t.Fatalf("produced: got %d messages", len(produced))
	}
	if produced[1].Role != wire.RoleAssistant || produced[1].Text() != "hello" {
		t.Fatalf("unexpected assistant message: %+v", produced[1])
	}
	if countEvents(events, wire.AgentEventEnd) != 1 {
		t.Fatal("missing agent_end")
	}
	calls := p.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls: got %d", len(calls))
	}
	if calls[0].Messages[0].Role != wire.RoleSystem || calls[0].Messages[0].Text() != "you are a test" {
		t.Fatalf("system prompt was not leading: %+v", calls[0].Messages)
	}
}

func TestToolRun(t *testing.T) {
	echo := &funcTool{name: "echo"}
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call_1", "echo", json.RawMessage(`{"text":"hi"}`)),
		}},
		faux.Response{Text: "done"},
	)
	produced, events, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{echo},
	}, wire.UserMessage("run echo"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := len(echo.Calls()); got != 1 {
		t.Fatalf("tool calls: got %d", got)
	}
	if len(p.Calls()) != 2 {
		t.Fatalf("provider calls: got %d", len(p.Calls()))
	}

	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
			break
		}
	}
	if result == nil {
		t.Fatal("missing tool result message")
	}
	if result.ToolCallID != "call_1" || result.ToolName != "echo" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Text() != "echo result" || result.IsErrorResult() {
		t.Fatalf("unexpected result content: %+v", result)
	}
	if countEvents(events, wire.AgentEventToolExecEnd) != 1 {
		t.Fatal("missing tool_execution_end")
	}
	if last := produced[len(produced)-1]; last.Text() != "done" {
		t.Fatalf("final text: got %q", last.Text())
	}
}

func TestUnknownTool(t *testing.T) {
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call_1", "missing", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "recovered"},
	)
	produced, _, err := runAgent(t, agent.Config{Model: testModel, Provider: p}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
		}
	}
	if result == nil || !result.IsErrorResult() {
		t.Fatalf("expected an error result, got %+v", result)
	}
	if !strings.Contains(result.Text(), "not found") {
		t.Fatalf("unexpected error text: %q", result.Text())
	}
	if last := produced[len(produced)-1]; last.Text() != "recovered" {
		t.Fatalf("loop did not continue: %q", last.Text())
	}
}

func TestParallelCompletionOrder(t *testing.T) {
	slow := &funcTool{name: "slow", delay: 80 * time.Millisecond}
	fast := &funcTool{name: "fast", delay: 5 * time.Millisecond}
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "slow", json.RawMessage(`{}`)),
			wire.ToolCallContent("c2", "fast", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "done"},
	)
	produced, events, err := runAgent(t, agent.Config{
		Model:       testModel,
		Provider:    p,
		Tools:       []tool.Tool{slow, fast},
		MaxParallel: 4,
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var endOrder []string
	for _, event := range events {
		if event.Type == wire.AgentEventToolExecEnd {
			endOrder = append(endOrder, event.ToolName)
		}
	}
	if len(endOrder) != 2 || endOrder[0] != "fast" || endOrder[1] != "slow" {
		t.Fatalf("end order: got %v, want [fast slow]", endOrder)
	}

	var resultOrder []string
	for _, message := range produced {
		if message.Role == wire.RoleToolResult {
			resultOrder = append(resultOrder, message.ToolName)
		}
	}
	if len(resultOrder) != 2 || resultOrder[0] != "slow" || resultOrder[1] != "fast" {
		t.Fatalf("result order: got %v, want [slow fast]", resultOrder)
	}
}

func TestSequentialExecution(t *testing.T) {
	slow := &funcTool{name: "slow", delay: 20 * time.Millisecond}
	fast := &funcTool{name: "fast", delay: 1 * time.Millisecond}
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "slow", json.RawMessage(`{}`)),
			wire.ToolCallContent("c2", "fast", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "done"},
	)
	_, events, err := runAgent(t, agent.Config{
		Model:         testModel,
		Provider:      p,
		Tools:         []tool.Tool{slow, fast},
		ToolExecution: agent.ToolExecutionSequential,
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var endOrder []string
	for _, event := range events {
		if event.Type == wire.AgentEventToolExecEnd {
			endOrder = append(endOrder, event.ToolName)
		}
	}
	if len(endOrder) != 2 || endOrder[0] != "slow" || endOrder[1] != "fast" {
		t.Fatalf("end order: got %v, want [slow fast]", endOrder)
	}
}

func TestSteeringInjectedBeforeNextRequest(t *testing.T) {
	p := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	polls := 0
	produced, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		GetSteeringMessages: func() []wire.Message {
			polls++
			if polls == 2 {
				return []wire.Message{wire.UserMessage("steer")}
			}
			return nil
		},
	}, wire.UserMessage("start"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(p.Calls()) != 2 {
		t.Fatalf("provider calls: got %d", len(p.Calls()))
	}
	if len(produced) != 4 {
		t.Fatalf("produced: got %d messages", len(produced))
	}
	if produced[2].Role != wire.RoleUser || produced[2].Text() != "steer" {
		t.Fatalf("steering message not injected: %+v", produced[2])
	}
	if produced[3].Text() != "second" {
		t.Fatalf("second response: got %q", produced[3].Text())
	}
}

func TestFollowUpAfterStop(t *testing.T) {
	p := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	served := false
	produced, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		GetFollowUpMessages: func() []wire.Message {
			if served {
				return nil
			}
			served = true
			return []wire.Message{wire.UserMessage("follow up")}
		},
	}, wire.UserMessage("start"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(p.Calls()) != 2 {
		t.Fatalf("provider calls: got %d", len(p.Calls()))
	}
	if len(produced) != 4 || produced[2].Text() != "follow up" {
		t.Fatalf("follow up not injected: %+v", produced)
	}
}

func TestBeforeToolCallBlocks(t *testing.T) {
	recorder := &funcTool{name: "echo"}
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "echo", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "done"},
	)
	produced, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{recorder},
		BeforeToolCall: func(_ context.Context, call tool.Call) (*agent.Block, error) {
			return &agent.Block{Block: true, Reason: "denied by policy"}, nil
		},
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(recorder.Calls()) != 0 {
		t.Fatal("blocked tool still executed")
	}
	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
		}
	}
	if result == nil || !result.IsErrorResult() || !strings.Contains(result.Text(), "denied by policy") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAfterToolCallRewrites(t *testing.T) {
	recorder := &funcTool{name: "echo"}
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "echo", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "done"},
	)
	produced, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{recorder},
		AfterToolCall: func(_ context.Context, call tool.Call, result *tool.Result) {
			result.Content = []wire.Content{wire.TextContent("rewritten")}
			result.IsError = true
		},
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
		}
	}
	if result == nil || result.Text() != "rewritten" || !result.IsErrorResult() {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestTerminateStopsRun(t *testing.T) {
	stopper := &funcTool{name: "stop", terminate: true}
	p := faux.New(faux.Response{ToolCalls: []wire.Content{
		wire.ToolCallContent("c1", "stop", json.RawMessage(`{}`)),
	}})
	_, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{stopper},
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(p.Calls()) != 1 {
		t.Fatalf("expected no follow-up request, got %d", len(p.Calls()))
	}
	if len(stopper.Calls()) != 1 {
		t.Fatal("stopper did not run")
	}
}

func TestLengthStopFailsToolCalls(t *testing.T) {
	recorder := &funcTool{name: "echo"}
	p := faux.New(faux.Response{
		Text:       "truncated",
		ToolCalls:  []wire.Content{wire.ToolCallContent("c1", "echo", json.RawMessage(`{"text":"x"}`))},
		StopReason: wire.StopLength,
	})
	produced, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{recorder},
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(recorder.Calls()) != 0 {
		t.Fatal("truncated tool call was executed")
	}
	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
		}
	}
	if result == nil || !strings.Contains(result.Text(), "output token limit") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestToolErrorBecomesErrorResult(t *testing.T) {
	failing := &funcTool{name: "fail", err: errors.New("disk on fire")}
	p := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "fail", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "recovered"},
	)
	produced, _, err := runAgent(t, agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{failing},
	}, wire.UserMessage("go"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
		}
	}
	if result == nil || !result.IsErrorResult() || !strings.Contains(result.Text(), "disk on fire") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// blockingProvider never finishes its stream until the context is cancelled.
type blockingProvider struct{}

func (blockingProvider) API() string { return "blocking" }

func (blockingProvider) Stream(ctx context.Context, _ provider.Request) (provider.Stream, error) {
	return &blockingStream{ctx: ctx}, nil
}

type blockingStream struct {
	ctx  context.Context
	once sync.Once
}

func (s *blockingStream) Recv() (wire.AssistantEvent, error) {
	started := false
	s.once.Do(func() { started = true })
	if started {
		partial := wire.Message{
			Role:       wire.RoleAssistant,
			Content:    wire.NewBlocks(),
			Timestamp:  wire.Now(),
			StopReason: wire.StopPending,
		}
		return wire.AssistantEvent{Type: wire.EventStart, Partial: &partial}, nil
	}
	<-s.ctx.Done()
	return wire.AssistantEvent{}, s.ctx.Err()
}

func (s *blockingStream) Close() error { return nil }

// speakingProvider says the given text and then waits to be stopped, which is
// what a stream looks like when a reader interrupts it.
type speakingProvider struct {
	thinking string
	text     string
}

func (speakingProvider) API() string { return "speaking" }

func (p speakingProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	frame := provider.NewFrame(req.Model)
	events := []wire.AssistantEvent{frame.Start()}
	if p.thinking != "" {
		events = append(events, frame.ThinkingStart(0), frame.ThinkingDelta(0, p.thinking), frame.ThinkingEnd(0))
	}
	if p.text != "" {
		index := 0
		if p.thinking != "" {
			index = 1
		}
		events = append(events, frame.TextStart(index), frame.TextDelta(index, p.text))
	}
	return &speakingStream{ctx: ctx, events: events}, nil
}

type speakingStream struct {
	ctx    context.Context
	events []wire.AssistantEvent
	next   int
}

func (s *speakingStream) Recv() (wire.AssistantEvent, error) {
	if s.next < len(s.events) {
		event := s.events[s.next]
		s.next++
		return event, nil
	}
	<-s.ctx.Done()
	return wire.AssistantEvent{}, s.ctx.Err()
}

func (s *speakingStream) Close() error { return nil }

// A reader who stops a turn keeps what they have already read. Thinking goes
// with it: the API only accepts a thinking block back together with the
// signature that arrives at its end, and this one never arrived.
func TestAbortKeepsWhatWasSaid(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	a := agent.New(agent.Config{
		Model:    testModel,
		Provider: speakingProvider{thinking: "let me think", text: "once upon a time"},
	})
	produced, err := a.Run(ctx, []wire.Message{wire.UserMessage("go")}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	last := produced[len(produced)-1]
	if last.StopReason != wire.StopAborted {
		t.Fatalf("stop reason: got %q", last.StopReason)
	}
	if last.Text() != "once upon a time" {
		t.Fatalf("the interrupted answer was not kept: %q", last.Text())
	}
	for _, block := range last.Content.Items() {
		if block.Type != wire.ContentText {
			t.Fatalf("an interrupted answer kept a %q block", block.Type)
		}
	}
}

// halfWritingTool reports what it has written and then waits to be stopped. The
// result has to say both things: that the call was interrupted, and how far it
// got before that.
type halfWritingTool struct{ produced string }

func (t *halfWritingTool) Name() string        { return "write" }
func (t *halfWritingTool) Label() string       { return "write" }
func (t *halfWritingTool) Description() string { return "test tool write" }

func (t *halfWritingTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (t *halfWritingTool) Execute(ctx context.Context, _ tool.Call, progress func(tool.Partial)) (tool.Result, error) {
	progress(tool.Partial{Content: []wire.Content{wire.TextContent(t.produced)}})
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func TestInterruptedToolResultKeepsItsOutput(t *testing.T) {
	p := faux.New(faux.Response{ToolCalls: []wire.Content{
		wire.ToolCallContent("call_1", "write", json.RawMessage(`{}`)),
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	a := agent.New(agent.Config{
		Model:    testModel,
		Provider: p,
		Tools:    []tool.Tool{&halfWritingTool{produced: "wrote 3 of 10 lines"}},
	})
	produced, err := a.Run(ctx, []wire.Message{wire.UserMessage("go")}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}

	var result *wire.Message
	for i := range produced {
		if produced[i].Role == wire.RoleToolResult {
			result = &produced[i]
		}
	}
	if result == nil {
		t.Fatal("the interrupted turn recorded no tool result")
	}
	if !result.Interrupted {
		t.Fatalf("the result does not say it was interrupted: %+v", result)
	}
	if !result.IsErrorResult() {
		t.Fatalf("an interrupted call is not a success: %+v", result)
	}
	if text := result.Text(); !strings.Contains(text, "Operation aborted") || !strings.Contains(text, "wrote 3 of 10 lines") {
		t.Fatalf("the result did not keep both halves: %q", text)
	}
	// The batch was stopped, so the turn that follows it was too: the run ends
	// with the aborted marker rather than an answer.
	if last := produced[len(produced)-1]; last.StopReason != wire.StopAborted {
		t.Fatalf("tail stop reason: %q", last.StopReason)
	}
}

func TestAbortProducesAbortedMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	a := agent.New(agent.Config{Model: testModel, Provider: blockingProvider{}})
	var events []wire.AgentEvent
	produced, err := a.Run(ctx, []wire.Message{wire.UserMessage("go")}, func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if len(produced) < 2 {
		t.Fatalf("produced: got %d messages", len(produced))
	}
	last := produced[len(produced)-1]
	if last.StopReason != wire.StopAborted {
		t.Fatalf("stop reason: got %q", last.StopReason)
	}
	if countEvents(events, wire.AgentEventEnd) != 1 {
		t.Fatal("missing agent_end after abort")
	}
}

// A run with no prompts is "continue": one request against the context the agent
// already holds. An overflow recovery needs exactly that — appending the prompt
// again would ask the same question twice.
func TestRunWithoutPromptsContinues(t *testing.T) {
	p := faux.New(
		faux.Response{Text: "first"},
		faux.Response{Text: "second"},
	)
	a := agent.New(agent.Config{Model: testModel, Provider: p})

	first, err := a.Run(context.Background(), []wire.Message{wire.UserMessage("hi")}, nil)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first run produced %d messages, want the prompt and the answer", len(first))
	}

	var events []wire.AgentEvent
	second, err := a.Run(context.Background(), nil, func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second) != 1 || second[0].Role != wire.RoleAssistant || second[0].Text() != "second" {
		t.Fatalf("second run produced %+v, want only the answer", second)
	}
	if got := countEvents(events, wire.AgentEventMessageStart); got != 1 {
		t.Fatalf("message_start events: got %d, want only the answer's", got)
	}
	if countEvents(events, wire.AgentEventEnd) != 1 {
		t.Fatal("missing agent_end on the continued run")
	}

	calls := p.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls: got %d want two", len(calls))
	}
	// The second request is built from the context the first one left behind, so
	// the prompt is neither repeated nor dropped.
	if got := len(calls[1].Messages); got != 2 {
		t.Fatalf("second request carried %d messages, want the first exchange", got)
	}
	if calls[1].Messages[0].Text() != "hi" || calls[1].Messages[1].Text() != "first" {
		t.Fatalf("second request: %+v", calls[1].Messages)
	}
}

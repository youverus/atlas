package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/compact"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// readTimeout bounds every wait on the server, so a protocol bug fails the test
// instead of hanging the suite.
const readTimeout = 5 * time.Second

func testModel() wire.Model {
	return wire.Model{ID: "faux-1", Name: "Faux One", API: "faux", Provider: "faux"}
}

// serveAll resolves every provider/model pair to prov, keeping the requested
// names so a switch is visible in the answer.
func serveAll(prov provider.Provider) app.Resolver {
	return func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		model := testModel()
		model.Provider = providerName
		model.ID = modelID
		return model, prov, nil
	}
}

// blockTool holds a turn inside a tool call until the test releases it or the
// turn is cancelled.
type blockTool struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockTool() *blockTool {
	return &blockTool{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockTool) Name() string        { return "block" }
func (b *blockTool) Label() string       { return "Block" }
func (b *blockTool) Description() string { return "Block until released." }

func (b *blockTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (b *blockTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return tool.Result{Content: []wire.Content{wire.TextContent("released")}}, nil
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
}

// frame is one JSONL record from the server.
type frame struct {
	Type    string          `json:"type"`
	ID      *string         `json:"id"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`

	// raw keeps every key, so a test can assert that an optional field is
	// absent rather than merely zero.
	raw map[string]any
}

func (f frame) data(t *testing.T, v any) {
	t.Helper()
	if len(f.Data) == 0 {
		t.Fatalf("response %q carries no data", f.Command)
	}
	if err := json.Unmarshal(f.Data, v); err != nil {
		t.Fatalf("decode data of %q: %v", f.Command, err)
	}
}

// frameBuffer decouples the server's writes from the test's reads.
//
// An io.Pipe write blocks until someone reads, so a test that waits for the
// kernel to reach a tool would deadlock the server before it ever gets there.
// The buffer is unbounded on purpose: the events that arrive while the test is
// not looking have to go somewhere.
type frameBuffer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	lines   [][]byte
	closed  bool
	expired bool
}

func newFrameBuffer() *frameBuffer {
	buffer := &frameBuffer{}
	buffer.cond = sync.NewCond(&buffer.mu)
	return buffer
}

func (b *frameBuffer) push(line []byte) {
	b.mu.Lock()
	b.lines = append(b.lines, line)
	b.mu.Unlock()
	b.cond.Signal()
}

func (b *frameBuffer) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cond.Broadcast()
}

// pop returns the next record, waiting at most timeout. It reports false when
// the stream closed or the wait expired, so a protocol bug fails the test
// instead of hanging the suite.
func (b *frameBuffer) pop(timeout time.Duration) ([]byte, bool) {
	timer := time.AfterFunc(timeout, func() {
		b.mu.Lock()
		b.expired = true
		b.mu.Unlock()
		b.cond.Broadcast()
	})
	defer timer.Stop()

	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.lines) == 0 && !b.closed && !b.expired {
		b.cond.Wait()
	}
	if len(b.lines) == 0 {
		b.expired = false
		return nil, false
	}
	b.expired = false
	line := b.lines[0]
	b.lines = b.lines[1:]
	return line, true
}

// harness runs a server against an in-memory app over pipes.
type harness struct {
	t      *testing.T
	app    *app.App
	prov   provider.Provider
	frames *frameBuffer
	in     *io.PipeWriter
	out    *io.PipeReader
	cancel context.CancelFunc
	// done closes when Serve returns, and serveErr holds why. A channel of
	// errors cannot be awaited twice, and both the test and the cleanup need
	// to wait.
	done     chan struct{}
	serveErr error
}

func newHarness(t *testing.T, prov provider.Provider, configure ...func(*app.Config)) *harness {
	t.Helper()

	cfg := app.Config{
		Model:        testModel(),
		Provider:     prov,
		SystemPrompt: app.StaticPrompt("base prompt"),
	}
	for _, fn := range configure {
		fn(&cfg)
	}
	manager, err := session.InMemory(t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application, err := app.New(cfg, manager)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	h := &harness{
		t:      t,
		app:    application,
		prov:   prov,
		frames: newFrameBuffer(),
		in:     inWriter,
		out:    outReader,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go func() {
		h.serveErr = New(application).Serve(ctx, inReader, outWriter)
		outWriter.Close()
		close(h.done)
	}()
	go func() {
		defer h.frames.close()
		reader := bufio.NewReader(outReader)
		for {
			line, err := reader.ReadBytes('\n')
			if trimmed := bytes.TrimSuffix(line, []byte("\n")); len(bytes.TrimSpace(trimmed)) > 0 {
				h.frames.push(trimmed)
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		inWriter.Close()
		cancel()
		outReader.Close()
		select {
		case <-h.done:
		case <-time.After(readTimeout):
			t.Error("the server did not stop")
		}
		h.frames.close()
	})
	return h
}

// send writes one command line.
func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatalf("send %q: %v", line, err)
	}
}

// sendJSON marshals a command and writes it.
func (h *harness) sendJSON(command map[string]any) {
	h.t.Helper()
	encoded, err := json.Marshal(command)
	if err != nil {
		h.t.Fatalf("marshal command: %v", err)
	}
	h.send(string(encoded))
}

// read returns the next record.
func (h *harness) read() frame {
	h.t.Helper()
	line, ok := h.frames.pop(readTimeout)
	if !ok {
		h.t.Fatal("no record arrived: the server closed its output or stopped writing")
	}
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		h.t.Fatalf("decode %q: %v", line, err)
	}
	if err := json.Unmarshal(line, &f.raw); err != nil {
		h.t.Fatalf("decode %q: %v", line, err)
	}
	return f
}

// response reads records until the answer to command arrives, discarding the
// events a turn streams in between.
func (h *harness) response(command string) frame {
	h.t.Helper()
	for {
		f := h.read()
		if f.Type != "response" {
			continue
		}
		if f.Command != command {
			h.t.Fatalf("response for %q arrived while waiting for %q", f.Command, command)
		}
		return f
	}
}

// success asserts a successful response and returns it.
func (h *harness) success(command string) frame {
	h.t.Helper()
	f := h.response(command)
	if f.Success == nil || !*f.Success {
		h.t.Fatalf("response %q failed: %s", command, f.Error)
	}
	return f
}

// failure asserts a failed response and returns it.
func (h *harness) failure(command string) frame {
	h.t.Helper()
	f := h.response(command)
	if f.Success == nil || *f.Success {
		h.t.Fatalf("response %q succeeded, want a failure", command)
	}
	return f
}

// events reads records until the event named last arrives, and returns every
// event including it. A response in between is a protocol error: a turn streams
// events, it does not answer commands.
func (h *harness) events(last string) []frame {
	h.t.Helper()
	var collected []frame
	for {
		f := h.read()
		if f.Type == "response" {
			h.t.Fatalf("response for %q arrived before event %q", f.Command, last)
		}
		collected = append(collected, f)
		if f.Type == last {
			return collected
		}
	}
}

func TestServeReportsState(t *testing.T) {
	h := newHarness(t, faux.New())
	h.send(`{"id":"1","type":"get_state"}`)

	f := h.success("get_state")
	if f.ID == nil || *f.ID != "1" {
		t.Fatalf("id = %v, want 1", f.ID)
	}

	var state struct {
		Model                 wire.Model `json:"model"`
		ThinkingLevel         string     `json:"thinkingLevel"`
		IsStreaming           bool       `json:"isStreaming"`
		IsCompacting          bool       `json:"isCompacting"`
		SteeringMode          string     `json:"steeringMode"`
		FollowUpMode          string     `json:"followUpMode"`
		SessionFile           *string    `json:"sessionFile"`
		SessionID             string     `json:"sessionId"`
		SessionName           *string    `json:"sessionName"`
		AutoCompactionEnabled *bool      `json:"autoCompactionEnabled"`
		MessageCount          int        `json:"messageCount"`
		PendingMessageCount   int        `json:"pendingMessageCount"`
	}
	f.data(t, &state)

	if state.Model.ID != "faux-1" || state.Model.Provider != "faux" {
		t.Fatalf("model = %+v, want the configured one", state.Model)
	}
	if state.ThinkingLevel != "off" {
		t.Fatalf("thinking level = %q, want off", state.ThinkingLevel)
	}
	if state.IsStreaming || state.IsCompacting {
		t.Fatalf("streaming/compacting = %v/%v, want both false", state.IsStreaming, state.IsCompacting)
	}
	if state.SteeringMode != string(app.QueueModeOneAtATime) || state.FollowUpMode != string(app.QueueModeOneAtATime) {
		t.Fatalf("modes = %q/%q, want the defaults", state.SteeringMode, state.FollowUpMode)
	}
	if state.SessionID == "" {
		t.Fatal("state has no session id")
	}
	if state.AutoCompactionEnabled == nil || !*state.AutoCompactionEnabled {
		t.Fatalf("autoCompactionEnabled = %v, want pi's default of true", state.AutoCompactionEnabled)
	}
	if state.MessageCount != 0 || state.PendingMessageCount != 0 {
		t.Fatalf("counts = %d/%d, want 0/0", state.MessageCount, state.PendingMessageCount)
	}
	// An in-memory session has no file, and pi omits the key rather than
	// reporting an empty path.
	if _, ok := f.raw["data"].(map[string]any)["sessionFile"]; ok {
		t.Fatal("an in-memory session reported a sessionFile")
	}
	if _, ok := f.raw["data"].(map[string]any)["sessionName"]; ok {
		t.Fatal("an unnamed session reported a sessionName")
	}
}

func TestServePromptStreamsAndPersists(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "the answer"}))

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "hi"})

	// pi acknowledges a prompt before the first event: the caller learns the
	// prompt was accepted without waiting for the model.
	ack := h.response("prompt")
	if ack.Success == nil || !*ack.Success {
		t.Fatalf("prompt failed: %s", ack.Error)
	}
	var disposition struct {
		Disposition string `json:"disposition"`
	}
	ack.data(t, &disposition)
	if disposition.Disposition != dispositionStarted {
		t.Fatalf("disposition = %q, want %q", disposition.Disposition, dispositionStarted)
	}

	events := h.events("agent_end")
	if events[0].Type != wire.AgentEventStart {
		t.Fatalf("first event = %q, want agent_start", events[0].Type)
	}

	var ends, updates int
	for _, event := range events {
		switch event.Type {
		case wire.AgentEventMessageEnd:
			ends++
		case wire.AgentEventMessageUpdate:
			updates++
			assistant, ok := event.raw["assistantMessageEvent"].(map[string]any)
			if !ok {
				t.Fatalf("message_update has no assistantMessageEvent: %v", event.raw)
			}
			if _, ok := assistant["partial"]; ok {
				t.Fatal("message_update leaked the accumulated partial")
			}
			if _, ok := event.raw["usage"]; !ok {
				t.Fatal("message_update has no usage")
			}
		}
	}
	if ends != 2 {
		t.Fatalf("message_end events = %d, want the prompt and the answer", ends)
	}
	if updates == 0 {
		t.Fatal("no message_update events were streamed")
	}

	h.send(`{"type":"get_messages"}`)
	var messages struct {
		Messages []wire.Message `json:"messages"`
	}
	h.success("get_messages").data(t, &messages)
	if len(messages.Messages) != 2 {
		t.Fatalf("messages = %d, want the prompt and the answer", len(messages.Messages))
	}
	if messages.Messages[0].Role != wire.RoleUser || messages.Messages[0].Text() != "hi" {
		t.Fatalf("first message = %+v, want the prompt", messages.Messages[0])
	}
	if messages.Messages[1].Role != wire.RoleAssistant || messages.Messages[1].Text() != "the answer" {
		t.Fatalf("second message = %+v, want the answer", messages.Messages[1])
	}

	h.send(`{"type":"get_last_assistant_text"}`)
	var last struct {
		Text string `json:"text"`
	}
	h.success("get_last_assistant_text").data(t, &last)
	if last.Text != "the answer" {
		t.Fatalf("last assistant text = %q, want the answer", last.Text)
	}
}

func TestServePromptWhileStreaming(t *testing.T) {
	block := newBlockTool()
	h := newHarness(t, faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "after steering"},
	), func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
	})

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "start"})
	ack := h.response("prompt")
	if ack.Success == nil || !*ack.Success {
		t.Fatalf("prompt failed: %s", ack.Error)
	}

	// The turn is inside the tool here.
	<-block.started

	h.sendJSON(map[string]any{"id": "s1", "type": "get_state"})
	var state struct {
		IsStreaming bool `json:"isStreaming"`
	}
	h.success("get_state").data(t, &state)
	if !state.IsStreaming {
		t.Fatal("state reports idle while a turn is running")
	}

	// A prompt without a behavior is ambiguous, and pi refuses it instead of
	// guessing whether the caller meant to correct the run or queue a turn.
	h.sendJSON(map[string]any{"id": "p2", "type": "prompt", "message": "late"})
	failure := h.failure("prompt")
	if failure.Error != errAlreadyProcessing {
		t.Fatalf("error = %q, want pi's wording", failure.Error)
	}
	if failure.ID == nil || *failure.ID != "p2" {
		t.Fatalf("id = %v, want p2", failure.ID)
	}

	h.sendJSON(map[string]any{"id": "p3", "type": "prompt", "message": "steer", "streamingBehavior": "steer"})
	var disposition struct {
		Disposition string `json:"disposition"`
	}
	h.success("prompt").data(t, &disposition)
	if disposition.Disposition != dispositionQueued {
		t.Fatalf("disposition = %q, want queued", disposition.Disposition)
	}

	h.sendJSON(map[string]any{"id": "p4", "type": "prompt", "message": "later", "streamingBehavior": "followUp"})
	h.success("prompt").data(t, &disposition)
	if disposition.Disposition != dispositionQueued {
		t.Fatalf("disposition = %q, want queued", disposition.Disposition)
	}

	close(block.release)
	h.events("agent_end")

	// The steering message is delivered before the next request, the follow-up
	// only when the agent would stop, so both end up in the transcript.
	h.send(`{"type":"get_messages"}`)
	var messages struct {
		Messages []wire.Message `json:"messages"`
	}
	h.success("get_messages").data(t, &messages)
	var texts []string
	for _, message := range messages.Messages {
		if message.Role == wire.RoleUser {
			texts = append(texts, message.Text())
		}
	}
	want := []string{"start", "steer", "later"}
	if len(texts) != len(want) {
		t.Fatalf("user messages = %q, want %q", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("user messages = %q, want %q", texts, want)
		}
	}
}

func TestServeAbortStopsTheTurn(t *testing.T) {
	block := newBlockTool()
	h := newHarness(t, faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
	), func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
	})

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "start"})
	h.success("prompt")
	<-block.started

	h.sendJSON(map[string]any{"id": "a1", "type": "abort"})

	// Abort waits for the run to settle, so the events it produced arrive
	// before its own response.
	var events []frame
	for {
		f := h.read()
		if f.Type == "response" {
			if f.Command != "abort" {
				t.Fatalf("response for %q arrived while waiting for abort", f.Command)
			}
			if f.Success == nil || !*f.Success {
				t.Fatalf("abort failed: %s", f.Error)
			}
			break
		}
		events = append(events, f)
	}
	if len(events) == 0 || events[len(events)-1].Type != wire.AgentEventEnd {
		t.Fatalf("last event before abort = %q, want agent_end", events[len(events)-1].Type)
	}

	h.send(`{"type":"get_state"}`)
	var state struct {
		IsStreaming bool `json:"isStreaming"`
	}
	h.success("get_state").data(t, &state)
	if state.IsStreaming {
		t.Fatal("state still reports a run after abort")
	}

	h.send(`{"type":"get_messages"}`)
	var messages struct {
		Messages []wire.Message `json:"messages"`
	}
	h.success("get_messages").data(t, &messages)
	final := messages.Messages[len(messages.Messages)-1]
	if final.Role != wire.RoleAssistant || final.StopReason != wire.StopAborted {
		t.Fatalf("final message = %+v, want an aborted assistant message", final)
	}
}

func TestServeClearQueue(t *testing.T) {
	h := newHarness(t, faux.New())

	h.sendJSON(map[string]any{"id": "s", "type": "steer", "message": "steer me"})
	h.success("steer")
	h.sendJSON(map[string]any{"id": "f", "type": "follow_up", "message": "follow me"})
	h.success("follow_up")

	h.send(`{"type":"get_state"}`)
	var state struct {
		PendingMessageCount int `json:"pendingMessageCount"`
	}
	h.success("get_state").data(t, &state)
	if state.PendingMessageCount != 2 {
		t.Fatalf("pending = %d, want 2", state.PendingMessageCount)
	}

	h.send(`{"type":"clear_queue"}`)
	var cleared struct {
		Steering []string `json:"steering"`
		FollowUp []string `json:"followUp"`
	}
	h.success("clear_queue").data(t, &cleared)
	if len(cleared.Steering) != 1 || cleared.Steering[0] != "steer me" {
		t.Fatalf("steering = %q, want the discarded message", cleared.Steering)
	}
	if len(cleared.FollowUp) != 1 || cleared.FollowUp[0] != "follow me" {
		t.Fatalf("followUp = %q, want the discarded message", cleared.FollowUp)
	}

	h.send(`{"type":"get_state"}`)
	h.success("get_state").data(t, &state)
	if state.PendingMessageCount != 0 {
		t.Fatalf("pending = %d, want 0", state.PendingMessageCount)
	}
}

func TestServeUnsupportedAndUnknownCommands(t *testing.T) {
	h := newHarness(t, faux.New())

	h.send(`{"id":"1","type":"export_html"}`)
	failure := h.failure("export_html")
	if failure.Error != "export_html is not implemented by this kernel" {
		t.Fatalf("error = %q, want the gap spelled out", failure.Error)
	}

	h.send(`{"id":"2","type":"teleport"}`)
	failure = h.failure("teleport")
	if failure.Error != "Unknown command: teleport" {
		t.Fatalf("error = %q, want pi's wording", failure.Error)
	}
}

func TestServeReportsParseErrors(t *testing.T) {
	h := newHarness(t, faux.New())

	h.send("not json")
	failure := h.failure("parse")
	if failure.ID != nil {
		t.Fatalf("id = %v, want none for a line that never parsed", failure.ID)
	}
	if !bytes.HasPrefix([]byte(failure.Error), []byte("Failed to parse command: ")) {
		t.Fatalf("error = %q, want pi's prefix", failure.Error)
	}

	// A blank line is a record too, and it does not parse.
	h.send("")
	h.failure("parse")
}

func TestServeSwitchesModelAndThinkingLevel(t *testing.T) {
	prov := faux.New(faux.Response{Text: "answer"})
	h := newHarness(t, prov, func(cfg *app.Config) {
		cfg.Resolver = serveAll(prov)
	})

	h.sendJSON(map[string]any{"type": "set_model", "provider": "other", "modelId": "m-2"})
	var model wire.Model
	h.success("set_model").data(t, &model)
	if model.Provider != "other" || model.ID != "m-2" {
		t.Fatalf("model = %+v, want other/m-2", model)
	}

	h.send(`{"type":"get_state"}`)
	var state struct {
		Model wire.Model `json:"model"`
	}
	h.success("get_state").data(t, &state)
	if state.Model.ID != "m-2" {
		t.Fatalf("state model = %+v, want the switched one", state.Model)
	}

	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "high"})
	response := h.success("set_thinking_level")
	if len(response.Data) != 0 {
		t.Fatalf("data = %s, want none", response.Data)
	}

	h.send(`{"type":"get_state"}`)
	var level struct {
		ThinkingLevel string `json:"thinkingLevel"`
	}
	h.success("get_state").data(t, &level)
	if level.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", level.ThinkingLevel)
	}

	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "extreme"})
	if failure := h.failure("set_thinking_level"); failure.Error != "Unknown thinking level: extreme" {
		t.Fatalf("error = %q, want the rejected level", failure.Error)
	}

	h.send(`{"type":"get_available_thinking_levels"}`)
	var levels struct {
		Levels []string `json:"levels"`
	}
	h.success("get_available_thinking_levels").data(t, &levels)
	if len(levels.Levels) != len(thinkingLevels) {
		t.Fatalf("levels = %q, want %q", levels.Levels, thinkingLevels)
	}
}

// switchTable is a model list as a front end would configure it: one model that
// thinks on a three-gear axis, and one that cannot think. The second one is what
// separates "no axis" from "not in the list".
func switchTable() []app.ModelOption {
	return []app.ModelOption{
		{
			Model:  wire.Model{ID: "m-1", Name: "M One", API: "faux", Provider: "p", Reasoning: true},
			Levels: []string{"off", "low", "high"},
		},
		{
			Model: wire.Model{ID: "m-2", Name: "M Two", API: "faux", Provider: "p"},
		},
	}
}

// tableFitter is the hook a front end supplies: it rounds a level onto the row's
// axis and refuses a gear for a model with no axis. It is a test double on
// purpose, so the transport is exercised against the contract rather than
// against the command line's own rounding.
func tableFitter(table []app.ModelOption) app.ThinkingFitter {
	return func(model wire.Model, level string) (string, string, error) {
		for _, option := range table {
			if option.Model.Provider != model.Provider || option.Model.ID != model.ID {
				continue
			}
			if len(option.Levels) == 0 {
				if level == "off" {
					return level, "", nil
				}
				return "", "", fmt.Errorf("%s does not think, so only off can be sent", model.ID)
			}
			if slices.Contains(option.Levels, level) {
				return level, "", nil
			}
			return option.Levels[len(option.Levels)-1], "rounded to the nearest gear", nil
		}
		return level, "", nil
	}
}

func TestServeCyclesModelAndThinkingLevel(t *testing.T) {
	table := switchTable()
	prov := faux.New()
	h := newHarness(t, prov, func(cfg *app.Config) {
		cfg.Model = table[0].Model
		cfg.Models = table
		cfg.Resolver = serveAll(prov)
		cfg.FitThinking = tableFitter(table)
	})

	h.send(`{"type":"get_available_models"}`)
	var available struct {
		Models []wire.Model `json:"models"`
	}
	h.success("get_available_models").data(t, &available)
	if len(available.Models) != len(table) {
		t.Fatalf("models = %+v, want the configured list", available.Models)
	}
	if available.Models[1].ID != "m-2" {
		t.Fatalf("models = %+v, want the list in configured order", available.Models)
	}

	h.send(`{"type":"get_available_thinking_levels"}`)
	var levels struct {
		Levels []string `json:"levels"`
	}
	h.success("get_available_thinking_levels").data(t, &levels)
	if strings.Join(levels.Levels, ",") != "off,low,high" {
		t.Fatalf("levels = %q, want the model's own axis", levels.Levels)
	}

	// A cycle reports the pair, and lands the level where the new model can
	// send it: m-2 cannot think, so holding a gear would describe a request
	// the next turn will not make.
	h.send(`{"type":"cycle_model"}`)
	var cycle struct {
		Model         wire.Model `json:"model"`
		ThinkingLevel string     `json:"thinkingLevel"`
		IsScoped      *bool      `json:"isScoped"`
	}
	h.success("cycle_model").data(t, &cycle)
	if cycle.Model.ID != "m-2" {
		t.Fatalf("model = %+v, want the next entry", cycle.Model)
	}
	if cycle.ThinkingLevel != "off" {
		t.Fatalf("thinkingLevel = %q, want off for a model that cannot think", cycle.ThinkingLevel)
	}
	if cycle.IsScoped == nil || *cycle.IsScoped {
		t.Fatalf("isScoped = %v, want false", cycle.IsScoped)
	}

	// The axis is the one in effect, and a model that cannot think offers
	// exactly the switch.
	h.send(`{"type":"get_available_thinking_levels"}`)
	h.success("get_available_thinking_levels").data(t, &levels)
	if strings.Join(levels.Levels, ",") != "off" {
		t.Fatalf("levels = %q, want [off]", levels.Levels)
	}

	// Nothing to cycle on that axis, which pi reports as null rather than as
	// a failure or an omitted key.
	h.send(`{"type":"cycle_thinking_level"}`)
	if value, present := h.success("cycle_thinking_level").raw["data"]; !present || value != nil {
		t.Fatalf("data = %v, want an explicit null", value)
	}

	// Cycling back fits the level it carried onto the axis it lands on.
	h.send(`{"type":"cycle_model"}`)
	h.success("cycle_model").data(t, &cycle)
	if cycle.Model.ID != "m-1" {
		t.Fatalf("model = %+v, want the wrap back to the first entry", cycle.Model)
	}

	// A gear the model cannot send is fitted, not refused, and the transcript
	// reports what was fitted.
	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "max"})
	if f := h.success("set_thinking_level"); len(f.Data) != 0 {
		t.Fatalf("data = %s, want none", f.Data)
	}
	h.send(`{"type":"get_state"}`)
	var state struct {
		ThinkingLevel string `json:"thinkingLevel"`
	}
	h.success("get_state").data(t, &state)
	if state.ThinkingLevel != "high" {
		t.Fatalf("thinkingLevel = %q, want the fitted gear", state.ThinkingLevel)
	}

	// A gear sent to a model that cannot think stays an error: there is no
	// request it could become, and rounding it to off would report success for
	// a level the model never receives.
	h.send(`{"type":"cycle_model"}`)
	h.success("cycle_model")
	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "high"})
	if failure := h.failure("set_thinking_level"); !strings.Contains(failure.Error, "does not think") {
		t.Fatalf("error = %q, want the missing axis named", failure.Error)
	}

	// Off is the switch rather than a gear, so it is accepted everywhere.
	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "off"})
	h.success("set_thinking_level")

	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "extreme"})
	if failure := h.failure("set_thinking_level"); failure.Error != "Unknown thinking level: extreme" {
		t.Fatalf("error = %q, want the rejected level", failure.Error)
	}
}

func TestServeReportsNothingToCycle(t *testing.T) {
	table := switchTable()[:1]
	h := newHarness(t, faux.New(), func(cfg *app.Config) {
		cfg.Model = table[0].Model
		cfg.Models = table
		cfg.FitThinking = tableFitter(table)
	})

	// One model is not a ring, and pi answers null instead of failing.
	h.send(`{"type":"cycle_model"}`)
	if value, present := h.success("cycle_model").raw["data"]; !present || value != nil {
		t.Fatalf("data = %v, want an explicit null", value)
	}

	// The axis still cycles where there is one.
	h.send(`{"type":"cycle_thinking_level"}`)
	var cycle struct {
		Level string `json:"level"`
	}
	h.success("cycle_thinking_level").data(t, &cycle)
	if cycle.Level != "low" {
		t.Fatalf("level = %q, want the next gear on the axis", cycle.Level)
	}
}

func TestServeRejectsUnknownModel(t *testing.T) {
	h := newHarness(t, faux.New(), func(cfg *app.Config) {
		cfg.Resolver = func(providerName, modelID string) (wire.Model, provider.Provider, error) {
			return wire.Model{}, nil, io.ErrClosedPipe
		}
	})

	h.sendJSON(map[string]any{"type": "set_model", "provider": "ghost", "modelId": "m-9"})
	if failure := h.failure("set_model"); failure.Error != "Model not found: ghost/m-9" {
		t.Fatalf("error = %q, want pi's wording", failure.Error)
	}
}

func TestServeQueueModes(t *testing.T) {
	h := newHarness(t, faux.New())

	h.sendJSON(map[string]any{"type": "set_steering_mode", "mode": "all"})
	h.success("set_steering_mode")
	h.sendJSON(map[string]any{"type": "set_follow_up_mode", "mode": "all"})
	h.success("set_follow_up_mode")

	h.send(`{"type":"get_state"}`)
	var state struct {
		SteeringMode string `json:"steeringMode"`
		FollowUpMode string `json:"followUpMode"`
	}
	h.success("get_state").data(t, &state)
	if state.SteeringMode != "all" || state.FollowUpMode != "all" {
		t.Fatalf("modes = %q/%q, want both all", state.SteeringMode, state.FollowUpMode)
	}

	h.sendJSON(map[string]any{"type": "set_steering_mode", "mode": "sometimes"})
	h.failure("set_steering_mode")
}

func TestServeSessionName(t *testing.T) {
	h := newHarness(t, faux.New())

	h.sendJSON(map[string]any{"type": "set_session_name", "name": "  plan  "})
	h.success("set_session_name")

	h.send(`{"type":"get_state"}`)
	var state struct {
		SessionName *string `json:"sessionName"`
	}
	h.success("get_state").data(t, &state)
	if state.SessionName == nil || *state.SessionName != "plan" {
		t.Fatalf("session name = %v, want plan", state.SessionName)
	}

	h.sendJSON(map[string]any{"type": "set_session_name", "name": "   "})
	if failure := h.failure("set_session_name"); failure.Error != "Session name cannot be empty" {
		t.Fatalf("error = %q, want the empty-name rejection", failure.Error)
	}
}

func TestServeEntriesAndTree(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "answer"}))

	h.sendJSON(map[string]any{"type": "prompt", "message": "hi"})
	h.success("prompt")
	h.events("agent_end")

	h.send(`{"type":"get_entries"}`)
	var entries struct {
		Entries []session.Entry `json:"entries"`
		LeafID  *string         `json:"leafId"`
	}
	h.success("get_entries").data(t, &entries)
	if len(entries.Entries) != 2 {
		t.Fatalf("entries = %d, want the prompt and the answer", len(entries.Entries))
	}
	if entries.LeafID == nil || *entries.LeafID != entries.Entries[1].ID {
		t.Fatalf("leaf = %v, want %q", entries.LeafID, entries.Entries[1].ID)
	}

	h.sendJSON(map[string]any{"type": "get_entries", "since": entries.Entries[0].ID})
	h.success("get_entries").data(t, &entries)
	if len(entries.Entries) != 1 {
		t.Fatalf("entries after the first = %d, want 1", len(entries.Entries))
	}

	h.sendJSON(map[string]any{"type": "get_entries", "since": "missing"})
	if failure := h.failure("get_entries"); failure.Error != "Entry not found: missing" {
		t.Fatalf("error = %q, want pi's wording", failure.Error)
	}

	h.send(`{"type":"get_tree"}`)
	var tree struct {
		Tree   []jsonTreeNode `json:"tree"`
		LeafID *string        `json:"leafId"`
	}
	h.success("get_tree").data(t, &tree)
	if len(tree.Tree) != 1 {
		t.Fatalf("roots = %d, want one", len(tree.Tree))
	}
	if len(tree.Tree[0].Children) != 1 {
		t.Fatalf("children = %d, want one", len(tree.Tree[0].Children))
	}
	if tree.LeafID == nil || *tree.LeafID != tree.Tree[0].Children[0].Entry.ID {
		t.Fatalf("leaf = %v, want the assistant entry", tree.LeafID)
	}
	// Every node carries a children array, even a leaf, so a client can walk
	// the tree without nil checks.
	encoded, err := json.Marshal(tree.Tree[0].Children[0])
	if err != nil {
		t.Fatalf("marshal leaf: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"children":[]`)) {
		t.Fatalf("leaf = %s, want an empty children array", encoded)
	}
}

func TestServeNewSession(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "answer"}))

	h.sendJSON(map[string]any{"type": "prompt", "message": "hi"})
	h.success("prompt")
	h.events("agent_end")

	h.send(`{"type":"new_session"}`)
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	h.success("new_session").data(t, &result)
	if result.Cancelled {
		t.Fatal("new_session reported cancelled")
	}

	h.send(`{"type":"get_messages"}`)
	var messages struct {
		Messages []wire.Message `json:"messages"`
	}
	h.success("get_messages").data(t, &messages)
	if len(messages.Messages) != 0 {
		t.Fatalf("messages = %d, want an empty conversation", len(messages.Messages))
	}

	h.send(`{"type":"get_state"}`)
	var state struct {
		MessageCount int  `json:"messageCount"`
		IsStreaming  bool `json:"isStreaming"`
	}
	h.success("get_state").data(t, &state)
	if state.MessageCount != 0 || state.IsStreaming {
		t.Fatalf("state = %+v, want a fresh session", state)
	}
}

// recordedSession writes a two-message session file whose cwd matches the
// harness app, which is the only kind switch_session accepts.
func (h *harness) recordedSession() *session.Manager {
	h.t.Helper()
	manager, err := session.Create(h.app.Session().Cwd(), h.t.TempDir(), session.Options{})
	if err != nil {
		h.t.Fatalf("create session: %v", err)
	}
	application, err := app.New(app.Config{
		Model:        testModel(),
		Provider:     faux.New(faux.Response{Text: "recorded"}),
		SystemPrompt: app.StaticPrompt("base prompt"),
	}, manager)
	if err != nil {
		h.t.Fatalf("new app: %v", err)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		h.t.Fatalf("ask: %v", err)
	}
	return manager
}

// switch_session continues a recorded session: the state that follows is the
// target's, file and all.
func TestServeSwitchSession(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "answer"}))
	target := h.recordedSession()

	h.sendJSON(map[string]any{"type": "switch_session", "sessionPath": target.File()})
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	h.success("switch_session").data(t, &result)
	if result.Cancelled {
		t.Fatal("switch_session reported cancelled")
	}

	h.send(`{"type":"get_state"}`)
	var state struct {
		SessionID    string `json:"sessionId"`
		SessionFile  string `json:"sessionFile"`
		MessageCount int    `json:"messageCount"`
	}
	h.success("get_state").data(t, &state)
	if state.SessionID != target.ID() || state.SessionFile != target.File() {
		t.Fatalf("state = %+v, want the target's session", state)
	}
	if state.MessageCount != 2 {
		t.Fatalf("messages = %d, want the recorded conversation (2)", state.MessageCount)
	}
}

// A path that does not exist stays an error: a client that resumes a typo must
// not get a fresh session instead.
func TestServeSwitchSessionReportsAMissingFile(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "answer"}))

	h.send(`{"type":"switch_session","sessionPath":"/tmp/atlas-no-such-session.jsonl"}`)
	if got := h.failure("switch_session").Error; !strings.Contains(got, "no session file") {
		t.Fatalf("error = %q, want the missing file", got)
	}
}

func TestServeSwitchSessionNeedsAPath(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "answer"}))

	h.send(`{"type":"switch_session"}`)
	if got := h.failure("switch_session").Error; !strings.Contains(got, "sessionPath is required") {
		t.Fatalf("error = %q, want the missing path", got)
	}
}

// The running turn is aborted first, so the switch cannot race a transcript
// that is still being written.
func TestServeSwitchSessionAbortsTheTurn(t *testing.T) {
	block := newBlockTool()
	h := newHarness(t, faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
	), func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
	})
	target := h.recordedSession()

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "start"})
	h.success("prompt")
	<-block.started

	h.sendJSON(map[string]any{"id": "s1", "type": "switch_session", "sessionPath": target.File()})
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	h.success("switch_session").data(t, &result)

	h.send(`{"type":"get_state"}`)
	var state struct {
		SessionID   string `json:"sessionId"`
		IsStreaming bool   `json:"isStreaming"`
	}
	h.success("get_state").data(t, &state)
	if state.SessionID != target.ID() || state.IsStreaming {
		t.Fatalf("state = %+v, want the switched session and no stream", state)
	}
}

// A follow-up queued while idle is delivered only after the turn stops, so it
// extends the run instead of interrupting it.
func TestServeFollowUpWhileIdle(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"}))

	h.sendJSON(map[string]any{"type": "follow_up", "message": "and then"})
	h.success("follow_up")

	h.sendJSON(map[string]any{"type": "prompt", "message": "start"})
	h.success("prompt")
	events := h.events("agent_end")

	var ends int
	for _, event := range events {
		if event.Type == wire.AgentEventMessageEnd {
			ends++
		}
	}
	if ends != 4 {
		t.Fatalf("message_end events = %d, want prompt, answer, follow-up, answer", ends)
	}

	h.send(`{"type":"get_last_assistant_text"}`)
	var last struct {
		Text string `json:"text"`
	}
	h.success("get_last_assistant_text").data(t, &last)
	if last.Text != "second" {
		t.Fatalf("last text = %q, want the follow-up's answer", last.Text)
	}
}

func TestServeGetCommandsIsEmpty(t *testing.T) {
	h := newHarness(t, faux.New())
	h.send(`{"type":"get_commands"}`)

	var commands struct {
		Commands []any `json:"commands"`
	}
	f := h.success("get_commands")
	f.data(t, &commands)
	if commands.Commands == nil || len(commands.Commands) != 0 {
		t.Fatalf("commands = %v, want an empty array", commands.Commands)
	}
	if !bytes.Contains(f.Data, []byte(`"commands":[]`)) {
		t.Fatalf("data = %s, want an empty array rather than null", f.Data)
	}
}

// stdin closing is how a client disconnects: the run is cancelled and Serve
// returns without leaving a goroutine behind.
func TestServeEOFStopsTheTurn(t *testing.T) {
	block := newBlockTool()
	h := newHarness(t, faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
	), func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
	})

	h.sendJSON(map[string]any{"type": "prompt", "message": "start"})
	h.success("prompt")
	<-block.started

	if err := h.in.Close(); err != nil {
		t.Fatalf("close input: %v", err)
	}
	select {
	case <-h.done:
		if h.serveErr != nil {
			t.Fatalf("serve: %v, want a clean stop", h.serveErr)
		}
	case <-time.After(readTimeout):
		t.Fatal("serve did not return after its input closed")
	}
}

func TestServePromptCarriesImages(t *testing.T) {
	prov := faux.New(faux.Response{Text: "seen"})
	h := newHarness(t, prov)

	h.sendJSON(map[string]any{
		"type":    "prompt",
		"message": "what is this",
		"images": []map[string]any{
			{"type": "image", "data": "AAAA", "mimeType": "image/png"},
		},
	})
	h.success("prompt")
	h.events("agent_end")

	calls := prov.Calls()
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
	var user *wire.Message
	for i := range calls[0].Messages {
		if calls[0].Messages[i].Role == wire.RoleUser {
			user = &calls[0].Messages[i]
		}
	}
	if user == nil {
		t.Fatal("the request carried no user message")
	}
	blocks := user.Content.Items()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want text and image", len(blocks))
	}
	if blocks[0].Type != wire.ContentText || blocks[0].Text != "what is this" {
		t.Fatalf("first block = %+v, want the text", blocks[0])
	}
	if blocks[1].Type != wire.ContentImage || blocks[1].Data != "AAAA" || blocks[1].MIMEType != "image/png" {
		t.Fatalf("second block = %+v, want the image", blocks[1])
	}
}

// A model switch during a run rebuilds the agent underneath it, so the kernel
// refuses it and the transport reports why.
func TestServeRejectsModelSwitchDuringTurn(t *testing.T) {
	block := newBlockTool()
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
	)
	h := newHarness(t, prov, func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
		cfg.Resolver = serveAll(prov)
	})

	h.sendJSON(map[string]any{"type": "prompt", "message": "start"})
	h.success("prompt")
	<-block.started

	h.sendJSON(map[string]any{"type": "set_model", "provider": "other", "modelId": "m-2"})
	if failure := h.failure("set_model"); failure.Error != app.ErrBusy.Error() {
		t.Fatalf("error = %q, want the busy error", failure.Error)
	}

	h.sendJSON(map[string]any{"type": "set_thinking_level", "level": "high"})
	if failure := h.failure("set_thinking_level"); failure.Error != app.ErrBusy.Error() {
		t.Fatalf("error = %q, want the busy error", failure.Error)
	}

	close(block.release)
	h.events("agent_end")
}

func TestServeRejectsUnknownStreamingBehavior(t *testing.T) {
	block := newBlockTool()
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
	)
	h := newHarness(t, prov, func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
	})

	h.sendJSON(map[string]any{"type": "prompt", "message": "start"})
	h.success("prompt")
	<-block.started

	h.sendJSON(map[string]any{"type": "prompt", "message": "late", "streamingBehavior": "later"})
	if failure := h.failure("prompt"); failure.Error != "Unknown streamingBehavior: later" {
		t.Fatalf("error = %q, want the rejected behavior", failure.Error)
	}

	close(block.release)
	h.events("agent_end")
}

func TestServeIgnoresExtensionUIResponses(t *testing.T) {
	h := newHarness(t, faux.New())

	// An extension UI answer names a request the kernel never made, so it has
	// nothing to answer and no response is written.
	h.send(`{"type":"extension_ui_response","id":"req-1","value":"yes"}`)

	h.send(`{"type":"get_state"}`)
	h.success("get_state")
}

func TestServeEmptySessionHasNoEntries(t *testing.T) {
	h := newHarness(t, faux.New())

	h.send(`{"type":"get_entries"}`)
	var entries struct {
		Entries []session.Entry `json:"entries"`
		LeafID  *string         `json:"leafId"`
	}
	f := h.success("get_entries")
	f.data(t, &entries)
	if entries.LeafID != nil {
		t.Fatalf("leaf = %v, want none", entries.LeafID)
	}
	if !bytes.Contains(f.Data, []byte(`"entries":[]`)) {
		t.Fatalf("data = %s, want an empty array rather than null", f.Data)
	}
}

func TestServeReturnsWriteFailures(t *testing.T) {
	h := newHarness(t, faux.New())

	// Closing the read side of the output pipe leaves the server writing into a
	// closed pipe, which is what a client that disconnected looks like.
	if err := h.out.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}
	h.send(`{"type":"get_state"}`)

	select {
	case <-h.done:
		if h.serveErr == nil {
			t.Fatal("serve returned no error after its output closed")
		}
	case <-time.After(readTimeout):
		t.Fatal("serve kept running with a closed output")
	}
}

func TestServeReturnsOnCancellation(t *testing.T) {
	h := newHarness(t, faux.New())

	h.cancel()
	select {
	case <-h.done:
		if h.serveErr == nil {
			t.Fatal("serve returned no error after cancellation")
		}
	case <-time.After(readTimeout):
		t.Fatal("serve ignored cancellation")
	}
}

// seedHistory appends assistant turns of about a thousand tokens each, enough
// for a small compaction setting to have a range to summarize.
func seedHistory(t *testing.T, h *harness, turns int) {
	t.Helper()
	manager := h.app.Session()
	for i := 0; i < turns; i++ {
		if _, err := manager.AppendMessage(wire.UserMessage(strings.Repeat("x", 4000))); err != nil {
			t.Fatalf("append prompt: %v", err)
		}
		if _, err := manager.AppendMessage(wire.AssistantMessage(wire.TextContent(strings.Repeat("y", 4000)))); err != nil {
			t.Fatalf("append answer: %v", err)
		}
	}
}

// smallCompaction keeps the last few messages so a short conversation still has
// a range to summarize.
func smallCompaction() *compact.Settings {
	return &compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 2000}
}

func TestServeCompactStreamsEventsAndAnswersWithTheCheckpoint(t *testing.T) {
	h := newHarness(t, faux.New(faux.Response{Text: "HISTORY", Usage: wire.Usage{Input: 3, Output: 2, TotalTokens: 5}}),
		func(cfg *app.Config) { cfg.Compaction = smallCompaction() })
	seedHistory(t, h, 3)

	h.sendJSON(map[string]any{"id": "1", "type": "compact", "customInstructions": "focus on the tests"})
	start := h.read()
	if start.Type != wire.AgentEventCompactionStart || start.raw["reason"] != wire.CompactionReasonManual {
		t.Fatalf("first record = %+v, want compaction_start", start.raw)
	}
	end := h.read()
	if end.Type != wire.AgentEventCompactionEnd {
		t.Fatalf("second record = %+v, want compaction_end", end.raw)
	}
	if end.raw["result"] == nil || end.raw["aborted"] != false {
		t.Fatalf("compaction_end = %+v", end.raw)
	}

	response := h.success("compact")
	var result wire.CompactionResult
	response.data(t, &result)
	if result.Summary != "HISTORY" {
		t.Fatalf("summary = %q", result.Summary)
	}
	if result.FirstKeptEntryID == "" || result.TokensBefore == 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.EstimatedTokensAfter == nil || *result.EstimatedTokensAfter <= 0 {
		t.Fatalf("estimated tokens after = %v", result.EstimatedTokensAfter)
	}

	// The instruction reached the summary request.
	calls := h.prov.(*faux.Provider).Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].Messages[1].Content.Text(), "Additional focus: focus on the tests") {
		t.Fatalf("summarization request: %+v", calls)
	}

	path := h.app.Session().Path("")
	if tip := path[len(path)-1]; tip.Type != session.TypeCompaction {
		t.Fatalf("tip = %q, want a compaction entry", tip.Type)
	}
}

func TestServeCompactReportsWhyItCannotCompact(t *testing.T) {
	h := newHarness(t, faux.New())

	h.send(`{"id":"1","type":"compact"}`)
	if start := h.read(); start.Type != wire.AgentEventCompactionStart {
		t.Fatalf("first record = %+v, want compaction_start", start.raw)
	}
	end := h.read()
	if end.Type != wire.AgentEventCompactionEnd {
		t.Fatalf("second record = %+v, want compaction_end", end.raw)
	}
	if end.raw["errorMessage"] != "Compaction failed: "+app.ErrNothingToCompact.Error() {
		t.Fatalf("compaction_end = %+v", end.raw)
	}
	failure := h.failure("compact")
	if failure.Error != app.ErrNothingToCompact.Error() {
		t.Fatalf("error = %q", failure.Error)
	}
}

// TestServeCompactAbortsTheRunningTurn pins pi's ordering: a manual compaction
// stops the turn instead of failing because one is in flight.
func TestServeCompactAbortsTheRunningTurn(t *testing.T) {
	block := newBlockTool()
	h := newHarness(t, faux.New(
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "block", []byte(`{}`))}},
		faux.Response{Text: "never used"},
	), func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
		cfg.Compaction = smallCompaction()
	})

	h.send(`{"id":"1","type":"prompt","message":"go"}`)
	h.success("prompt")
	select {
	case <-block.started:
	case <-time.After(readTimeout):
		t.Fatal("the turn never reached the tool")
	}

	h.send(`{"id":"2","type":"compact"}`)
	// The turn is aborted first, so the answer is a compaction failure rather
	// than "Agent is already processing".
	failure := h.failure("compact")
	if failure.Error != app.ErrNothingToCompact.Error() {
		t.Fatalf("error = %q, want the compaction to have run after the abort", failure.Error)
	}
}

func TestServeSetAutoCompaction(t *testing.T) {
	h := newHarness(t, faux.New())

	h.send(`{"id":"1","type":"set_auto_compaction","enabled":false}`)
	h.success("set_auto_compaction")
	if h.app.CompactionSettings().Enabled {
		t.Fatal("auto compaction is still enabled")
	}

	h.send(`{"id":"2","type":"get_state"}`)
	var state struct {
		AutoCompactionEnabled *bool `json:"autoCompactionEnabled"`
	}
	h.success("get_state").data(t, &state)
	if state.AutoCompactionEnabled == nil || *state.AutoCompactionEnabled {
		t.Fatalf("autoCompactionEnabled = %v, want false", state.AutoCompactionEnabled)
	}

	h.send(`{"id":"3","type":"set_auto_compaction"}`)
	if failure := h.failure("set_auto_compaction"); failure.Error != "enabled is required" {
		t.Fatalf("error = %q", failure.Error)
	}
}

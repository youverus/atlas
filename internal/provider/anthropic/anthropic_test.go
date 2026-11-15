package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/wire"
)

const testModel = "claude-x"

// captured is one request that reached the test server. The http.Request is not
// kept because its body is already consumed.
type captured struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// testServer replays a fixed response for every request and records what the
// adapter sent. Nothing here touches a network: httptest listens on loopback.
type testServer struct {
	*httptest.Server
	captured []captured
	handler  http.HandlerFunc
}

func newServer(t *testing.T, status int, body string) *testServer {
	t.Helper()
	server := &testServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		server.captured = append(server.captured, captured{
			method: req.Method,
			path:   req.URL.Path,
			header: req.Header.Clone(),
			body:   data,
		})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// sseServer replays a recorded stream from testdata/anthropic.
func sseServer(t *testing.T, name string) *testServer {
	t.Helper()
	return newServer(t, http.StatusOK, string(readFixture(t, name)))
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "anthropic", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func (s *testServer) last(t *testing.T) captured {
	t.Helper()
	if len(s.captured) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.captured[len(s.captured)-1]
}

func providerFor(server *testServer) *anthropic.Provider {
	return anthropic.New(anthropic.Compat{BaseURL: server.URL + "/v1"})
}

func messageRequest(messages ...wire.Message) provider.Request {
	return provider.Request{
		Model:    wire.Model{ID: testModel, API: anthropic.APIName, Provider: "anthropic"},
		Messages: messages,
	}
}

func collect(t *testing.T, p *anthropic.Provider, req provider.Request) (wire.Message, []wire.AssistantEvent) {
	t.Helper()
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	message, events, err := provider.Collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return message, events
}

func eventTypes(events []wire.AssistantEvent) []string {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}

func assertEventTypes(t *testing.T, events []wire.AssistantEvent, want ...string) {
	t.Helper()
	got := eventTypes(events)
	if len(want) == 0 {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types:\n got %v\nwant %v", got, want)
	}
}

// normalizeJSON re-encodes a JSON document with sorted keys so two documents
// can be compared as text and a mismatch prints as a readable diff.
func normalizeJSON(t *testing.T, label string, data []byte) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", label, err, data)
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("%s: re-encode: %v", label, err)
	}
	return string(out)
}

func decodeRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request %s: %v", body, err)
	}
	return payload
}

// requestMessages returns the messages array of a captured request body.
func requestMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	raw, ok := decodeRequest(t, body)["messages"].([]any)
	if !ok {
		t.Fatalf("request has no messages array: %s", body)
	}
	out := make([]map[string]any, len(raw))
	for i, item := range raw {
		message, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("message %d is not an object: %v", i, item)
		}
		out[i] = message
	}
	return out
}

func TestAPIName(t *testing.T) {
	if got := anthropic.New(anthropic.Compat{}).API(); got != anthropic.APIName {
		t.Fatalf("api: got %q", got)
	}
	if anthropic.APIName != "anthropic-messages" {
		// The routing key is part of the model declaration format, so it cannot
		// change without a migration.
		t.Fatalf("api name: got %q", anthropic.APIName)
	}
}

func TestStreamText(t *testing.T) {
	server := sseServer(t, "text.sse")
	req := messageRequest(wire.UserMessage("hi"))
	req.Options.APIKey = "sk-ant-test"
	message, events := collect(t, providerFor(server), req)

	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventTextStart,
		wire.EventTextDelta,
		wire.EventTextDelta,
		wire.EventTextEnd,
		wire.EventDone,
	)

	if got := message.Text(); got != "Hello world" {
		t.Fatalf("text: got %q", got)
	}
	if message.Role != wire.RoleAssistant {
		t.Fatalf("role: got %q", message.Role)
	}
	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.API != anthropic.APIName || message.Model != testModel || message.Provider != "anthropic" {
		t.Fatalf("identity: got api=%q model=%q provider=%q", message.API, message.Model, message.Provider)
	}
	if message.Timestamp == 0 {
		t.Fatal("timestamp: not set")
	}
	assertUsage(t, message.Usage, wire.Usage{Input: 11, Output: 7, CacheRead: 3, CacheWrite: 2, TotalTokens: 23})

	if index := events[2].ContentIndex; index == nil || *index != 0 {
		t.Fatalf("text delta index: got %v", index)
	}
	if events[2].Delta != "Hello" || events[3].Delta != " world" {
		t.Fatalf("deltas: got %q and %q", events[2].Delta, events[3].Delta)
	}
	if events[4].Content != "Hello world" {
		t.Fatalf("text end content: got %q", events[4].Content)
	}
}

// assertUsage compares the scalar fields and keeps pointer fields out of the
// comparison, since their addresses never match.
func assertUsage(t *testing.T, got *wire.Usage, want wire.Usage) {
	t.Helper()
	if got == nil {
		t.Fatal("usage: not set")
	}
	scalar := *got
	scalar.CacheWrite1h = nil
	scalar.Reasoning = nil
	if scalar != want {
		t.Fatalf("usage:\n got %+v\nwant %+v", scalar, want)
	}
}

func TestStreamThinking(t *testing.T) {
	server := sseServer(t, "thinking.sse")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventThinkingStart,
		wire.EventThinkingDelta,
		wire.EventThinkingEnd,
		wire.EventTextStart,
		wire.EventTextDelta,
		wire.EventTextEnd,
		wire.EventDone,
	)

	blocks := message.Content.Items()
	if len(blocks) != 2 {
		t.Fatalf("blocks: got %d\n%+v", len(blocks), blocks)
	}
	if blocks[0].Type != wire.ContentThinking || blocks[0].Thinking != "Let me think" {
		t.Fatalf("thinking block: %+v", blocks[0])
	}
	// Two signature_delta fragments must assemble into one signature.
	if got := blocks[0].ThinkingSignature; got != "sig-asig-b" {
		t.Fatalf("signature: got %q", got)
	}
	if blocks[1].Type != wire.ContentText || blocks[1].Text != "Done" {
		t.Fatalf("text block: %+v", blocks[1])
	}
	if message.Usage == nil || message.Usage.Reasoning == nil || *message.Usage.Reasoning != 8 {
		t.Fatalf("reasoning tokens: got %+v", message.Usage)
	}
}

func TestStreamToolUse(t *testing.T) {
	server := sseServer(t, "tool_use.sse")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventTextStart,
		wire.EventTextDelta,
		wire.EventTextEnd,
		wire.EventToolCallStart,
		wire.EventToolCallDelta,
		wire.EventToolCallDelta,
		wire.EventToolCallEnd,
		wire.EventDone,
	)

	if message.StopReason != wire.StopToolUse {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	blocks := message.Content.Items()
	if len(blocks) != 2 {
		t.Fatalf("blocks: got %d\n%+v", len(blocks), blocks)
	}
	call := blocks[1]
	if call.Type != wire.ContentToolCall || call.ID != "toolu_1" || call.Name != "read" {
		t.Fatalf("tool call: %+v", call)
	}
	if got := string(call.Arguments); got != `{"path":"a.go"}` {
		t.Fatalf("arguments: got %q", got)
	}
	// The end event carries a copy of the call so a UI does not need the partial.
	if events[7].ToolCall == nil || events[7].ToolCall.ID != "toolu_1" {
		t.Fatalf("toolcall_end payload: %+v", events[7].ToolCall)
	}
}

func TestRedactedThinking(t *testing.T) {
	server := sseServer(t, "redacted.sse")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	// The text block opens with "Hi" already in it and streams " there", so both
	// halves are delivered as deltas: a reader that counts deltas must see the
	// same text as one that reads the finished block.
	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventThinkingStart,
		wire.EventThinkingEnd,
		wire.EventTextStart,
		wire.EventTextDelta,
		wire.EventTextDelta,
		wire.EventTextEnd,
		wire.EventDone,
	)
	if got := message.Text(); got != "Hi there" {
		t.Fatalf("text: got %q; want %q", got, "Hi there")
	}

	block := message.Content.Items()[0]
	if block.Type != wire.ContentThinking {
		t.Fatalf("block type: got %q", block.Type)
	}
	if block.Thinking != "[Reasoning redacted]" {
		t.Fatalf("placeholder: got %q", block.Thinking)
	}
	if block.Redacted == nil || !*block.Redacted {
		t.Fatalf("redacted flag: got %v", block.Redacted)
	}
	if block.ThinkingSignature != "opaque-payload" {
		t.Fatalf("payload: got %q", block.ThinkingSignature)
	}
	// pause_turn means "resubmit", which is a normal stop for the loop.
	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
}

func TestErrorEvent(t *testing.T) {
	server := sseServer(t, "error.sse")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventTextStart,
		wire.EventTextDelta,
		wire.EventError,
	)
	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if !strings.Contains(message.ErrorMessage, "overloaded_error") || !strings.Contains(message.ErrorMessage, "Overloaded") {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
	// The text that did arrive stays in the message: a failed turn still shows
	// what the model managed to say.
	if got := message.Text(); got != "Partial" {
		t.Fatalf("partial text: got %q", got)
	}
}

func TestTruncatedStream(t *testing.T) {
	server := sseServer(t, "truncated.sse")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	last := events[len(events)-1]
	if last.Type != wire.EventError {
		t.Fatalf("terminal event: got %q", last.Type)
	}
	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "Anthropic stream ended without a stop reason" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
	if got := message.Text(); got != "Cut off" {
		t.Fatalf("partial text: got %q", got)
	}
}

func TestUnknownStopReason(t *testing.T) {
	server := sseServer(t, "unknown_stop.sse")
	message, _ := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "Unhandled stop reason: mystery" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}

func TestEmptyBody(t *testing.T) {
	server := newServer(t, http.StatusOK, "")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	assertEventTypes(t, events, wire.EventStart, wire.EventError)
	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
}

func TestInvalidToolArguments(t *testing.T) {
	server := sseServer(t, "tool_bad_json.sse")
	message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	assertEventTypes(t, events, wire.EventStart, wire.EventToolCallStart, wire.EventToolCallDelta, wire.EventError)
	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if !strings.Contains(message.ErrorMessage, "not valid JSON") {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}

func TestEventNameFallback(t *testing.T) {
	server := sseServer(t, "no_event_type.sse")
	message, _ := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	// A proxy that drops data.type leaves only the event name; the stream must
	// still decode instead of treating every event as unknown.
	if got := message.Text(); got != "By name" {
		t.Fatalf("text: got %q", got)
	}
	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
}

func TestRequestPathAndHeaders(t *testing.T) {
	server := sseServer(t, "text.sse")
	req := messageRequest(wire.UserMessage("hi"))
	req.Options.APIKey = "sk-ant-test"
	collect(t, providerFor(server), req)

	last := server.last(t)
	if last.method != http.MethodPost {
		t.Fatalf("method: got %q", last.method)
	}
	if last.path != "/v1/messages" {
		t.Fatalf("path: got %q", last.path)
	}
	if got := last.header.Get("x-api-key"); got != "sk-ant-test" {
		t.Fatalf("x-api-key: got %q", got)
	}
	if got := last.header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version: got %q", got)
	}
	if got := last.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type: got %q", got)
	}
	if got := last.header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept: got %q", got)
	}
}

func TestNoAPIKeyHeaderWhenUnset(t *testing.T) {
	server := sseServer(t, "text.sse")
	collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if got := server.last(t).header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key: got %q, want no header", got)
	}
}

func TestVersionAndHeaderOverrides(t *testing.T) {
	server := sseServer(t, "text.sse")
	p := anthropic.New(anthropic.Compat{BaseURL: server.URL + "/v1", Version: "2024-06-01"})
	req := messageRequest(wire.UserMessage("hi"))
	req.Options.Headers = map[string]string{"anthropic-beta": "feature-x"}
	collect(t, p, req)

	last := server.last(t)
	if got := last.header.Get("anthropic-version"); got != "2024-06-01" {
		t.Fatalf("anthropic-version: got %q", got)
	}
	if got := last.header.Get("anthropic-beta"); got != "feature-x" {
		t.Fatalf("anthropic-beta: got %q", got)
	}
}

func TestModelBaseURLWins(t *testing.T) {
	server := sseServer(t, "text.sse")
	p := anthropic.New(anthropic.Compat{BaseURL: "http://127.0.0.1:1/ignored"})
	req := messageRequest(wire.UserMessage("hi"))
	req.Model.BaseURL = server.URL + "/proxy"
	collect(t, p, req)

	if got := server.last(t).path; got != "/proxy/messages" {
		t.Fatalf("path: got %q", got)
	}
}

func TestHTTPErrorBodyShapes(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		contains []string
		code     string
	}{
		{
			name:     "anthropic error envelope",
			status:   http.StatusTooManyRequests,
			body:     `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			contains: []string{"429", "slow down"},
		},
		{
			name:     "bare message",
			status:   http.StatusBadRequest,
			body:     `{"message":"bad model"}`,
			contains: []string{"400", "bad model"},
		},
		{
			name:     "plain text",
			status:   http.StatusInternalServerError,
			body:     "boom",
			contains: []string{"500", "boom"},
		},
		{
			name:     "empty body",
			status:   http.StatusBadGateway,
			body:     "",
			contains: []string{"502"},
		},
		{
			name:     "context overflow",
			status:   http.StatusBadRequest,
			body:     `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`,
			contains: []string{"400", "prompt is too long"},
			code:     wire.ErrorCodeContextOverflow,
		},
		{
			name:     "byte size overflow",
			status:   http.StatusRequestEntityTooLarge,
			body:     `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`,
			contains: []string{"413", "Request exceeds the maximum size"},
			code:     wire.ErrorCodeContextOverflow,
		},
		{
			name:     "rate limit is not overflow",
			status:   http.StatusTooManyRequests,
			body:     `{"type":"error","error":{"type":"rate_limit_error","message":"too many tokens per minute, slow down"}}`,
			contains: []string{"429"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newServer(t, test.status, test.body)
			// A rejected request is a message, not a Go error: callers keep one
			// read path.
			message, events := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

			assertEventTypes(t, events, wire.EventStart, wire.EventError)
			if message.StopReason != wire.StopError {
				t.Fatalf("stop reason: got %q", message.StopReason)
			}
			for _, want := range test.contains {
				if !strings.Contains(message.ErrorMessage, want) {
					t.Fatalf("error message %q does not contain %q", message.ErrorMessage, want)
				}
			}
			if message.ErrorCode != test.code {
				t.Fatalf("error code: got %q want %q", message.ErrorCode, test.code)
			}
		})
	}
}

// TestCancelWhileWaitingForHeaders covers the window before the response
// headers arrive: the handler accepts the request and never answers, so the
// cancellation has to abort the call itself, not the stream reader.
func TestCancelWhileWaitingForHeaders(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	server := &testServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		close(arrived)
		<-release
	}))
	t.Cleanup(server.Close)
	// Registered after the server so it runs first: Close waits for the handler,
	// so the handler has to be released before it.
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()

	stream, err := providerFor(server).Stream(ctx, messageRequest(wire.UserMessage("hi")))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer stream.Close()

	message, events, err := provider.Collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	assertEventTypes(t, events, wire.EventStart, wire.EventError)
	if message.StopReason != wire.StopAborted {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
}

// TestCancelBeforeRequest pins that an already cancelled context fails the same
// way without reaching the server.
func TestCancelBeforeRequest(t *testing.T) {
	server := sseServer(t, "text.sse")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream, err := providerFor(server).Stream(ctx, messageRequest(wire.UserMessage("hi")))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	message, events, err := provider.Collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	assertEventTypes(t, events, wire.EventStart, wire.EventError)
	if message.StopReason != wire.StopAborted {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if len(server.captured) != 0 {
		t.Fatalf("a cancelled request reached the server: %s", server.captured[0].body)
	}
}

func TestCancelMidStream(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := &testServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `event: message_start`+"\n"+`data: {"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":1}}}`+"\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}))
	t.Cleanup(server.Close)
	// Released before Close, which waits for the handler to return.
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := providerFor(server).Stream(ctx, messageRequest(wire.UserMessage("hi")))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer stream.Close()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("server never wrote the first event")
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first event: %v", err)
	}
	cancel()

	var terminal wire.AssistantEvent
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		terminal = event
		if event.Type == wire.EventDone || event.Type == wire.EventError {
			break
		}
	}
	if terminal.Type != wire.EventError || terminal.Reason != string(wire.StopAborted) {
		t.Fatalf("terminal event: got %q reason %q", terminal.Type, terminal.Reason)
	}
}

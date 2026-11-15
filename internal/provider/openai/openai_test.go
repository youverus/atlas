package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
)

const testModel = "gpt-x"

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

// sseServer replays a recorded stream from testdata/sse.
func sseServer(t *testing.T, name string) *testServer {
	t.Helper()
	return newServer(t, http.StatusOK, string(readFixture(t, name)))
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "sse", name)
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

func providerFor(server *testServer) *openai.Provider {
	return openai.New(openai.Compat{BaseURL: server.URL})
}

func chatRequest(messages ...wire.Message) provider.Request {
	return provider.Request{
		Model:    wire.Model{ID: testModel, API: openai.APIName, Provider: "openai"},
		Messages: messages,
	}
}

func collect(t *testing.T, p *openai.Provider, req provider.Request) (wire.Message, []wire.AssistantEvent) {
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

// assistantMessages returns the assistant messages of a captured request body.
func assistantMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, message := range requestMessages(t, body) {
		if message["role"] == "assistant" {
			out = append(out, message)
		}
	}
	if len(out) == 0 {
		t.Fatalf("request has no assistant message: %s", body)
	}
	return out
}

func TestAPIName(t *testing.T) {
	if got := openai.New(openai.Compat{}).API(); got != openai.APIName {
		t.Fatalf("api: got %q", got)
	}
	if openai.APIName != "openai-completions" {
		// The routing key is part of the model declaration format, so it cannot
		// change without a migration.
		t.Fatalf("api name: got %q", openai.APIName)
	}
}

func TestStreamText(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	req := chatRequest(wire.UserMessage("hi"))
	req.Options.APIKey = "sk-test"
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
	if message.API != openai.APIName || message.Model != testModel || message.Provider != "openai" {
		t.Fatalf("identity: got api=%q model=%q provider=%q", message.API, message.Model, message.Provider)
	}
	if message.Timestamp == 0 {
		t.Fatal("timestamp: not set")
	}
	if message.Usage == nil {
		t.Fatal("usage: not set")
	}
	wantUsage := wire.Usage{Input: 9, Output: 2, TotalTokens: 11}
	if *message.Usage != wantUsage {
		t.Fatalf("usage:\n got %+v\nwant %+v", *message.Usage, wantUsage)
	}

	if index := events[2].ContentIndex; index == nil || *index != 0 {
		t.Fatalf("text delta index: got %v", index)
	}
	if events[2].Delta != "Hello" || events[3].Delta != " world" {
		t.Fatalf("deltas: got %q and %q", events[2].Delta, events[3].Delta)
	}
	if events[4].Content != "Hello world" {
		t.Fatalf("text end content: got %q", events[4].Content)
	}

	sent := server.last(t)
	if sent.method != http.MethodPost {
		t.Fatalf("method: got %q", sent.method)
	}
	if sent.path != "/chat/completions" {
		t.Fatalf("path: got %q", sent.path)
	}
	for name, want := range map[string]string{
		"Authorization": "Bearer sk-test",
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
	} {
		if got := sent.header.Get(name); got != want {
			t.Fatalf("%s: got %q want %q", name, got, want)
		}
	}

	payload := decodeRequest(t, sent.body)
	if payload["stream"] != true {
		t.Fatalf("stream: got %v", payload["stream"])
	}
	if got, want := normalizeJSON(t, "stream_options", mustJSON(t, payload["stream_options"])), "{\n  \"include_usage\": true\n}"; got != want {
		t.Fatalf("stream_options:\n got %s\nwant %s", got, want)
	}
	if _, ok := payload["tools"]; ok {
		t.Fatalf("tools: sent %v for a transcript without tool declarations", payload["tools"])
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return data
}

func TestStreamToolCalls(t *testing.T) {
	server := sseServer(t, "openai_tool_calls.sse")
	message, events := collect(t, providerFor(server), chatRequest(wire.UserMessage("list files")))

	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventToolCallStart,
		wire.EventToolCallStart,
		wire.EventToolCallDelta,
		wire.EventToolCallDelta,
		wire.EventToolCallDelta,
		wire.EventToolCallDelta,
		wire.EventToolCallEnd,
		wire.EventToolCallEnd,
		wire.EventDone,
	)

	if message.StopReason != wire.StopToolUse {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.Usage != nil {
		t.Fatalf("usage: got %+v for a stream without a usage trailer", *message.Usage)
	}

	calls := message.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("tool calls: got %d", len(calls))
	}
	for i, want := range []struct{ id, name, arguments string }{
		{"call_a", "read", `{"path":"a.txt"}`},
		{"call_b", "grep", `{"pattern":"x"}`},
	} {
		call := calls[i]
		if call.ID != want.id || call.Name != want.name || string(call.Arguments) != want.arguments {
			t.Fatalf("tool call %d:\n got %+v\nwant %+v", i, call, want)
		}
	}

	// The end event carries the finished call, which is what the loop dispatches.
	end := events[7]
	if end.ContentIndex == nil || *end.ContentIndex != 0 || end.ToolCall == nil {
		t.Fatalf("tool call end: got %+v", end)
	}
	if string(end.ToolCall.Arguments) != `{"path":"a.txt"}` {
		t.Fatalf("tool call end arguments: got %s", end.ToolCall.Arguments)
	}
}

func TestStreamReasoning(t *testing.T) {
	server := sseServer(t, "openai_reasoning.sse")
	p := openai.New(openai.Compat{BaseURL: server.URL})
	message, events := collect(t, p, chatRequest(wire.UserMessage("read a.txt")))

	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	items := message.Content.Items()
	if len(items) != 2 {
		t.Fatalf("blocks: got %d (%+v)", len(items), items)
	}
	if items[0].Type != wire.ContentThinking || items[0].Thinking != "We need to read the file." {
		t.Fatalf("thinking block: got %+v", items[0])
	}
	// The signature records which vendor field carried the text, so a later
	// request replays it under the same name.
	if items[0].ThinkingSignature != "reasoning_content" {
		t.Fatalf("thinking signature: got %q", items[0].ThinkingSignature)
	}
	if items[1].Type != wire.ContentText || items[1].Text != "Let me look." {
		t.Fatalf("text block: got %+v", items[1])
	}

	wantUsage := wire.Usage{Input: 8, CacheRead: 12, Output: 8, TotalTokens: 28}
	if message.Usage == nil {
		t.Fatal("usage: not set")
	}
	usage := *message.Usage
	usage.Reasoning = nil
	if usage != wantUsage {
		t.Fatalf("usage:\n got %+v\nwant %+v", usage, wantUsage)
	}
	if message.Usage.Reasoning == nil || *message.Usage.Reasoning != 5 {
		t.Fatalf("reasoning tokens: got %v", message.Usage.Reasoning)
	}

	// Thinking is block 0, text is block 1: the decoder allocates in stream
	// order rather than merging runs.
	for _, event := range events {
		switch event.Type {
		case wire.EventThinkingStart, wire.EventThinkingDelta, wire.EventThinkingEnd:
			if event.ContentIndex == nil || *event.ContentIndex != 0 {
				t.Fatalf("%s index: got %v", event.Type, event.ContentIndex)
			}
		case wire.EventTextStart, wire.EventTextDelta, wire.EventTextEnd:
			if event.ContentIndex == nil || *event.ContentIndex != 1 {
				t.Fatalf("%s index: got %v", event.Type, event.ContentIndex)
			}
		}
	}
	if start := events[1]; start.Type != wire.EventThinkingStart {
		t.Fatalf("second event: got %q", start.Type)
	}
}

func TestStreamWithoutFinishReason(t *testing.T) {
	server := sseServer(t, "openai_truncated.sse")
	message, events := collect(t, providerFor(server), chatRequest(wire.UserMessage("write a lot")))

	assertEventTypes(t, events,
		wire.EventStart,
		wire.EventTextStart,
		wire.EventTextDelta,
		wire.EventError,
	)
	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if !strings.Contains(message.ErrorMessage, "without finish_reason") {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
	// The partial text survives: the terminal message shows what arrived before
	// the connection died.
	if got := message.Text(); got != "half a sen" {
		t.Fatalf("partial text: got %q", got)
	}
}

func TestTolerateMissingFinishReason(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    wire.StopReason
		text    string
		toolUse bool
	}{
		{
			name: "text infers stop",
			body: string(readFixture(t, "openai_truncated.sse")),
			want: wire.StopStop,
			text: "half a sen",
		},
		{
			name: "tool calls infer toolUse",
			body: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\"," +
				"\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"a.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
				"data: [DONE]\n\n",
			want:    wire.StopToolUse,
			toolUse: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newServer(t, http.StatusOK, test.body)
			p := openai.New(openai.Compat{
				BaseURL:                     server.URL,
				TolerateMissingFinishReason: true,
			})
			message, events := collect(t, p, chatRequest(wire.UserMessage("go")))

			if message.StopReason != test.want {
				t.Fatalf("stop reason: got %q want %q", message.StopReason, test.want)
			}
			if got := message.Text(); got != test.text {
				t.Fatalf("text: got %q", got)
			}
			if last := events[len(events)-1].Type; last != wire.EventDone {
				t.Fatalf("terminal event: got %q", last)
			}
			if test.toolUse {
				if calls := message.ToolCalls(); len(calls) != 1 || calls[0].ID != "call_a" {
					t.Fatalf("tool calls: got %+v", calls)
				}
			}
		})
	}
}

func TestStreamErrorChunk(t *testing.T) {
	server := newServer(t, http.StatusOK, "data: {\"error\":{\"message\":\"overloaded\",\"type\":\"server_error\"}}\n\ndata: [DONE]\n\n")
	message, _ := collect(t, providerFor(server), chatRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if got, want := message.ErrorMessage, "openai: stream error (server_error): overloaded"; got != want {
		t.Fatalf("error message:\n got %q\nwant %q", got, want)
	}
}

func TestUnknownFinishReasonFails(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n" +
		"data: [DONE]\n\n"
	server := newServer(t, http.StatusOK, body)
	message, _ := collect(t, providerFor(server), chatRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if got, want := message.ErrorMessage, "Provider finish_reason: content_filter"; got != want {
		t.Fatalf("error message:\n got %q\nwant %q", got, want)
	}
}

func TestEmptyStreamFails(t *testing.T) {
	server := newServer(t, http.StatusOK, "")
	message, _ := collect(t, providerFor(server), chatRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if !strings.Contains(message.ErrorMessage, "no stream chunks") {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}

func TestBrokenToolCallFails(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing id",
			body: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0," +
				"\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
			want: "without an id",
		},
		{
			name: "arguments are not JSON",
			body: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\"," +
				"\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
			want: "not valid JSON",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newServer(t, http.StatusOK, test.body)
			message, _ := collect(t, providerFor(server), chatRequest(wire.UserMessage("go")))

			if message.StopReason != wire.StopError {
				t.Fatalf("stop reason: got %q", message.StopReason)
			}
			if !strings.Contains(message.ErrorMessage, test.want) {
				t.Fatalf("error message %q does not mention %q", message.ErrorMessage, test.want)
			}
		})
	}
}

// A gateway keep-alive is a named event; skipping it keeps the stream readable.
func TestNamedEventsAreIgnored(t *testing.T) {
	body := "event: ping\ndata: not json\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	server := newServer(t, http.StatusOK, body)
	message, _ := collect(t, providerFor(server), chatRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if got := message.Text(); got != "ok" {
		t.Fatalf("text: got %q", got)
	}
}

func TestHTTPErrorResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
		code   string
	}{
		{
			name:   "openai error envelope",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"slow down","type":"rate_limit"}}`,
			want:   "429 Too Many Requests: slow down",
		},
		{
			name:   "bare message field",
			status: http.StatusInternalServerError,
			body:   `{"message":"upstream exploded"}`,
			want:   "500 Internal Server Error: upstream exploded",
		},
		{
			name:   "plain text body",
			status: http.StatusBadGateway,
			body:   "bad gateway\n",
			want:   "502 Bad Gateway: bad gateway",
		},
		{
			name:   "empty body",
			status: http.StatusServiceUnavailable,
			body:   "",
			want:   "503 Service Unavailable",
		},
		{
			name:   "context overflow",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Your input exceeds the context window of this model","type":"invalid_request_error","code":"context_length_exceeded"}}`,
			want:   "400 Bad Request: Your input exceeds the context window of this model",
			code:   wire.ErrorCodeContextOverflow,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newServer(t, test.status, test.body)
			message, events := collect(t, providerFor(server), chatRequest(wire.UserMessage("hi")))

			assertEventTypes(t, events, wire.EventStart, wire.EventError)
			if message.StopReason != wire.StopError {
				t.Fatalf("stop reason: got %q", message.StopReason)
			}
			if message.ErrorMessage != test.want {
				t.Fatalf("error message:\n got %q\nwant %q", message.ErrorMessage, test.want)
			}
			if message.ErrorCode != test.code {
				t.Fatalf("error code: got %q want %q", message.ErrorCode, test.code)
			}
		})
	}
}

func TestAbortedRequest(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream, err := providerFor(server).Stream(ctx, chatRequest(wire.UserMessage("hi")))
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
	if events[1].Reason != string(wire.StopAborted) {
		t.Fatalf("event reason: got %q", events[1].Reason)
	}
}

// Stream only fails for payloads that cannot be expressed on the wire; nothing
// is sent in that case.
func TestStreamRejectsUnrepresentableRequests(t *testing.T) {
	tests := []struct {
		name     string
		messages []wire.Message
		want     string
	}{
		{
			name:     "tool result without call id",
			messages: []wire.Message{{Role: wire.RoleToolResult, ToolName: "echo", Content: wire.NewTextList("ok")}},
			want:     "no toolCallId",
		},
		{
			name:     "unknown role",
			messages: []wire.Message{{Role: wire.Role("narrator"), Content: wire.NewTextList("hi")}},
			want:     "unsupported message role",
		},
		{
			name:     "image without mime type",
			messages: []wire.Message{wire.UserMessageBlocks(wire.ImageContent("AAAA", ""))},
			want:     "mimeType",
		},
		{
			// Tool result images travel as a trailing user message, so they
			// need the same validation as a user image.
			name: "image without mime type in a tool result",
			messages: []wire.Message{wire.ToolResultMessage(
				"call_1", "read", false, wire.ImageContent("QUJD", ""),
			)},
			want: "mimeType",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := sseServer(t, "openai_text.sse")
			_, err := providerFor(server).Stream(context.Background(), chatRequest(test.messages...))
			if err == nil {
				t.Fatal("stream: got nil error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err, test.want)
			}
			if len(server.captured) != 0 {
				t.Fatalf("sent %d requests for an unrepresentable payload", len(server.captured))
			}
		})
	}
}

func TestRequestMatchesGolden(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	p := openai.New(openai.Compat{
		BaseURL:        server.URL,
		MaxTokensField: "max_completion_tokens",
	})

	system := wire.SystemMessage("You are pi.")
	system.ToolsAdded = []wire.Tool{{
		Name:        "echo",
		Description: "Echo the input back.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}}
	assistant := wire.AssistantMessage(
		wire.TextContent("working"),
		wire.ToolCallContent("call_1", "echo", json.RawMessage(`{"text":"hi"}`)),
	)

	req := chatRequest(
		system,
		wire.UserMessage("hello"),
		assistant,
		wire.ToolResultMessage("call_1", "echo", false, wire.TextContent("echo: hi")),
		wire.UserMessage("again"),
	)
	req.Options.Temperature = wire.Ptr(0.2)
	req.Options.MaxTokens = wire.Ptr(64)
	collect(t, p, req)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "openai", "request_tools.json")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	got := normalizeJSON(t, "request", server.last(t).body)
	want := normalizeJSON(t, "golden", golden)
	if got != want {
		t.Fatalf("request body mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMaxTokensFieldSelection(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		present string
		absent  string
	}{
		{name: "compatible host defaults to max_tokens", present: "max_tokens", absent: "max_completion_tokens"},
		{name: "override to max_completion_tokens", field: "max_completion_tokens", present: "max_completion_tokens", absent: "max_tokens"},
		{name: "override to max_tokens", field: "max_tokens", present: "max_tokens", absent: "max_completion_tokens"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := sseServer(t, "openai_text.sse")
			p := openai.New(openai.Compat{BaseURL: server.URL, MaxTokensField: test.field})
			req := chatRequest(wire.UserMessage("hi"))
			req.Options.MaxTokens = wire.Ptr(64)
			collect(t, p, req)

			payload := decodeRequest(t, server.last(t).body)
			if got := payload[test.present]; got != float64(64) {
				t.Fatalf("%s: got %v", test.present, got)
			}
			if _, ok := payload[test.absent]; ok {
				t.Fatalf("%s: sent as well", test.absent)
			}
		})
	}
}

func TestDisableStreamOptions(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	p := openai.New(openai.Compat{BaseURL: server.URL, DisableStreamOptions: true})
	collect(t, p, chatRequest(wire.UserMessage("hi")))

	if payload := decodeRequest(t, server.last(t).body); payload["stream_options"] != nil {
		t.Fatalf("stream_options: got %v", payload["stream_options"])
	}
}

func TestReasoningReplay(t *testing.T) {
	tests := []struct {
		name    string
		blocks  []wire.Content
		require bool
		want    map[string]any
	}{
		{
			name:   "reasoning_content signature",
			blocks: []wire.Content{wire.ThinkingContent("weigh options", "reasoning_content"), wire.TextContent("ok")},
			want:   map[string]any{"role": "assistant", "content": "ok", "reasoning_content": "weigh options"},
		},
		{
			name:   "reasoning signature",
			blocks: []wire.Content{wire.ThinkingContent("weigh options", "reasoning"), wire.TextContent("ok")},
			want:   map[string]any{"role": "assistant", "content": "ok", "reasoning": "weigh options"},
		},
		{
			name:   "unknown signature is not replayed",
			blocks: []wire.Content{wire.ThinkingContent("weigh options", ""), wire.TextContent("ok")},
			want:   map[string]any{"role": "assistant", "content": "ok"},
		},
		{
			name:    "require reasoning content fills an empty field",
			blocks:  []wire.Content{wire.TextContent("ok")},
			require: true,
			want:    map[string]any{"role": "assistant", "content": "ok", "reasoning_content": ""},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := sseServer(t, "openai_text.sse")
			p := openai.New(openai.Compat{BaseURL: server.URL, RequireReasoningContent: test.require})
			assistant := wire.AssistantMessage(test.blocks...)
			assistant.StopReason = wire.StopStop
			collect(t, p, chatRequest(wire.UserMessage("go"), assistant, wire.UserMessage("more")))

			messages := assistantMessages(t, server.last(t).body)
			got := normalizeJSON(t, "assistant", mustJSON(t, messages[0]))
			want := normalizeJSON(t, "want", mustJSON(t, test.want))
			if got != want {
				t.Fatalf("assistant message mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestUserImagePart(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	user := wire.UserMessageBlocks(
		wire.TextContent("what is this"),
		wire.ImageContent("QUJD", "image/png"),
	)
	collect(t, providerFor(server), chatRequest(user))

	messages := requestMessages(t, server.last(t).body)
	if len(messages) != 1 || messages[0]["role"] != "user" {
		t.Fatalf("messages: got %+v", messages)
	}
	got := normalizeJSON(t, "content", mustJSON(t, messages[0]["content"]))
	want := normalizeJSON(t, "want", []byte(`[
		{"type":"text","text":"what is this"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}
	]`))
	if got != want {
		t.Fatalf("user content mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestEmptyToolResultGetsPlaceholder(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	result := wire.ToolResultMessage("call_1", "echo", false)
	collect(t, providerFor(server), chatRequest(wire.UserMessage("go"), result))

	messages := requestMessages(t, server.last(t).body)
	if len(messages) != 2 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	if got := messages[1]["content"]; got != "(no tool output)" {
		t.Fatalf("tool content: got %v", got)
	}
	if got := messages[1]["tool_call_id"]; got != "call_1" {
		t.Fatalf("tool call id: got %v", got)
	}
}

// An assistant turn with neither text nor tool calls cannot be replayed, so it
// is dropped instead of being sent as an empty message.
func TestEmptyAssistantTurnIsDropped(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	aborted := wire.AssistantMessage()
	aborted.StopReason = wire.StopAborted
	collect(t, providerFor(server), chatRequest(wire.UserMessage("go"), aborted, wire.UserMessage("again")))

	messages := requestMessages(t, server.last(t).body)
	if len(messages) != 2 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	for _, message := range messages {
		if message["role"] == "assistant" {
			t.Fatalf("assistant message was sent: %+v", message)
		}
	}
}

func TestToolsComeFromSystemMessage(t *testing.T) {
	server := sseServer(t, "openai_text.sse")
	system := wire.SystemMessage("You are pi.")
	system.ToolsAdded = []wire.Tool{
		{Name: "echo", Description: "Echo.", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "noargs"},
	}
	collect(t, providerFor(server), chatRequest(system, wire.UserMessage("hi")))

	payload := decodeRequest(t, server.last(t).body)
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("tools: got %v", payload["tools"])
	}
	want := normalizeJSON(t, "tools", mustJSON(t, tools))
	expected := normalizeJSON(t, "want", []byte(`[
		{"type":"function","function":{"name":"echo","description":"Echo.","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"noargs","description":"","parameters":{"type":"object","properties":{}}}}
	]`))
	if want != expected {
		t.Fatalf("tools mismatch\n--- got ---\n%s\n--- want ---\n%s", want, expected)
	}
}

package responses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/responses"
	"pi-go/internal/wire"
)

// captured is one request that reached the test server. The http.Request is not
// kept because its body is already consumed.
type captured struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// testServer replays a fixed answer and records what the adapter sent. Nothing
// here touches a network: httptest listens on loopback.
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

func (s *testServer) last(t *testing.T) captured {
	t.Helper()
	if len(s.captured) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.captured[len(s.captured)-1]
}

// fixture reads a recorded stream.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "responses", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func sseServer(t *testing.T, name string) *testServer {
	t.Helper()
	return newServer(t, http.StatusOK, fixture(t, name))
}

// testModel is a plain model: no reasoning, text only.
func testModel(server *testServer) wire.Model {
	return wire.Model{
		ID:       "gpt-x",
		API:      responses.APIName,
		Provider: "openai",
		BaseURL:  server.URL + "/v1",
	}
}

func providerFor(server *testServer) provider.Provider {
	return responses.New(responses.Compat{BaseURL: server.URL + "/v1"})
}

func textRequest(model wire.Model, text string) provider.Request {
	return provider.Request{
		Model:    model,
		Messages: wire.NormalizeContext("You are pi.", nil, []wire.Message{wire.UserMessage(text)}),
	}
}

// collect drains a stream and returns the terminal message.
func collect(t *testing.T, p provider.Provider, request provider.Request) wire.Message {
	t.Helper()
	stream, err := p.Stream(context.Background(), request)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	message, _, err := provider.Collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return message
}

func normalizeJSON(t *testing.T, label string, data []byte) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", label, err, data)
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return string(pretty)
}

func TestEndpointAndHeaders(t *testing.T) {
	server := sseServer(t, "text.sse")
	request := textRequest(testModel(server), "hi")
	request.Options.APIKey = "sk-test"
	collect(t, providerFor(server), request)

	got := server.last(t)
	if got.method != http.MethodPost {
		t.Fatalf("method: got %s; want POST", got.method)
	}
	// The Responses API lives one path below the chat completions endpoint that
	// shares the same base URL.
	if got.path != "/v1/responses" {
		t.Fatalf("path: got %s; want /v1/responses", got.path)
	}
	if auth := got.header.Get("Authorization"); auth != "Bearer sk-test" {
		t.Fatalf("authorization: got %q", auth)
	}
	if accept := got.header.Get("Accept"); accept != "text/event-stream" {
		t.Fatalf("accept: got %q", accept)
	}
}

// TestStatelessRequest pins the decision the adapter is built on: the
// conversation lives in this process, so no request may name server-side state.
func TestStatelessRequest(t *testing.T) {
	server := sseServer(t, "text.sse")
	request := textRequest(testModel(server), "hi")
	request.Options.SessionID = "session-1"
	collect(t, providerFor(server), request)

	var body map[string]any
	if err := json.Unmarshal(server.last(t).body, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	store, ok := body["store"]
	if !ok {
		t.Fatal("the request does not say store at all; it must say false")
	}
	if store != false {
		t.Fatalf("store: got %v; want false", store)
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		if _, exists := body[field]; exists {
			t.Fatalf("the request carries %s; this adapter keeps the conversation itself", field)
		}
	}
	if key, _ := body["prompt_cache_key"].(string); key != "session-1" {
		t.Fatalf("prompt_cache_key: got %v; want the session id", body["prompt_cache_key"])
	}
}

// TestRequestShape compares the whole payload with a golden file, so a field that
// changes shape fails here rather than at a vendor.
func TestRequestShape(t *testing.T) {
	server := sseServer(t, "text.sse")
	model := testModel(server)
	temperature := 0.2
	maxTokens := 64
	request := provider.Request{
		Model: model,
		Messages: wire.NormalizeContext("You are pi.",
			[]wire.Tool{{
				Name:        "echo",
				Description: "Echo the input back.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			}},
			[]wire.Message{
				wire.UserMessage("hello"),
				{
					Role: wire.RoleAssistant,
					Content: wire.NewBlocks(
						wire.TextContent("working"),
						wire.ToolCallContent("call_1", "echo", json.RawMessage(`{"text":"hi"}`)),
					),
					StopReason: wire.StopToolUse,
				},
				wire.ToolResultMessage("call_1", "echo", false, wire.TextContent("echo: hi")),
				wire.UserMessage("again"),
			}),
		Options: provider.Options{Temperature: &temperature, MaxTokens: &maxTokens},
	}
	collect(t, providerFor(server), request)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "responses", "request_tools.json")
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

// TestReasoningFields pins the thinking dial, including the two cases that are
// easy to get wrong: an unset level on a reasoning model must disable reasoning
// (the API thinks by default), and only a running level asks for the encrypted
// text.
func TestReasoningFields(t *testing.T) {
	tests := []struct {
		name        string
		reasoning   bool
		level       string
		wantEffort  string
		wantType    string
		wantInclude bool
	}{
		{name: "no reasoning model sends nothing", reasoning: false, level: "high"},
		{name: "unset level disables", reasoning: true, level: "", wantEffort: "none", wantType: "developer"},
		{name: "off disables", reasoning: true, level: "off", wantEffort: "none", wantType: "developer"},
		{name: "high runs", reasoning: true, level: "high", wantEffort: "high", wantType: "developer", wantInclude: true},
		{name: "ultra clamps to max", reasoning: true, level: "ultra", wantEffort: "max", wantType: "developer", wantInclude: true},
		{name: "unknown takes medium", reasoning: true, level: "turbo", wantEffort: "medium", wantType: "developer", wantInclude: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := sseServer(t, "text.sse")
			model := testModel(server)
			model.Reasoning = test.reasoning
			request := textRequest(model, "hi")
			request.Options.ThinkingLevel = test.level
			collect(t, providerFor(server), request)

			var body struct {
				Reasoning *struct {
					Effort  string `json:"effort"`
					Summary string `json:"summary"`
				} `json:"reasoning"`
				Include []string `json:"include"`
				Input   []struct {
					Role string `json:"role"`
				} `json:"input"`
			}
			if err := json.Unmarshal(server.last(t).body, &body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if test.wantEffort == "" {
				if body.Reasoning != nil {
					t.Fatalf("reasoning: got %+v; want none", body.Reasoning)
				}
				if len(body.Include) > 0 {
					t.Fatalf("include: got %v; want none", body.Include)
				}
				return
			}
			if body.Reasoning == nil {
				t.Fatal("reasoning: got none; want an effort")
			}
			// The developer role is the API's preferred way to address a
			// reasoning model, so it travels with the same condition as the dial.
			if role := body.Input[0].Role; role != test.wantType {
				t.Fatalf("instruction role: got %q; want %q", role, test.wantType)
			}
			if body.Reasoning.Effort != test.wantEffort {
				t.Fatalf("effort: got %q; want %q", body.Reasoning.Effort, test.wantEffort)
			}
			if test.wantEffort == "none" && body.Reasoning.Summary != "" {
				t.Fatalf("summary: got %q; want none while reasoning is off", body.Reasoning.Summary)
			}
			if test.wantInclude {
				if len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
					t.Fatalf("include: got %v", body.Include)
				}
				if body.Reasoning.Summary != "auto" {
					t.Fatalf("summary: got %q; want auto", body.Reasoning.Summary)
				}
			}
		})
	}
}

// TestOutputTokenFloor covers the one value the API rejects outright.
func TestOutputTokenFloor(t *testing.T) {
	server := sseServer(t, "text.sse")
	request := textRequest(testModel(server), "hi")
	small := 4
	request.Options.MaxTokens = &small
	collect(t, providerFor(server), request)

	var body map[string]any
	if err := json.Unmarshal(server.last(t).body, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got, _ := body["max_output_tokens"].(float64); got != 16 {
		t.Fatalf("max_output_tokens: got %v; want 16", body["max_output_tokens"])
	}
}

// TestReasoningReplay covers the only way a store:false reasoning conversation
// survives: the item the server issued is handed back verbatim, and a signature
// this adapter did not write is not mistaken for one.
func TestReasoningReplay(t *testing.T) {
	server := sseServer(t, "text.sse")
	model := testModel(server)
	model.Reasoning = true
	item := `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"blob"}`
	request := provider.Request{
		Model: model,
		Messages: wire.NormalizeContext("You are pi.", nil, []wire.Message{
			wire.UserMessage("first"),
			wire.AssistantMessage(
				wire.ThinkingContent("thought", item),
				wire.TextContent("done"),
			),
			// A signature written by another adapter: replaying it as an item
			// would fail the whole request, so it is dropped.
			wire.AssistantMessage(wire.ThinkingContent("more", "reasoning_content")),
			wire.UserMessage("second"),
		}),
		Options: provider.Options{ThinkingLevel: "high"},
	}
	collect(t, providerFor(server), request)

	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(server.last(t).body, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}

	var reasoningItems []map[string]any
	for _, entry := range body.Input {
		if entry["type"] == "reasoning" {
			reasoningItems = append(reasoningItems, entry)
		}
	}
	if len(reasoningItems) != 1 {
		t.Fatalf("reasoning items: got %d; want 1\n%s", len(reasoningItems), server.last(t).body)
	}
	if got := reasoningItems[0]["encrypted_content"]; got != "blob" {
		t.Fatalf("encrypted_content: got %v; want the blob the server issued", got)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(item), &want); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	// Field for field, not byte for byte: the point is that nothing was lost.
	if !reflect.DeepEqual(reasoningItems[0], want) {
		t.Fatalf("reasoning item: got %v; want %v", reasoningItems[0], want)
	}
}

// TestToolResultOutput covers the two shapes a tool result can take, because the
// API rejects an empty one and a text-only model must not silently lose images.
func TestToolResultOutput(t *testing.T) {
	tests := []struct {
		name    string
		model   func(wire.Model) wire.Model
		content []wire.Content
		want    any
	}{
		{
			name:    "empty becomes a stand-in",
			content: nil,
			want:    "(no tool output)",
		},
		{
			name:    "text stays a string",
			content: []wire.Content{wire.TextContent("first"), wire.TextContent("second")},
			want:    "first\nsecond",
		},
		{
			name:    "an image the model takes becomes parts",
			content: []wire.Content{wire.TextContent("look"), wire.ImageContent("QUJD", "image/png")},
			want: []any{
				map[string]any{"type": "input_text", "text": "look"},
				map[string]any{"type": "input_image", "detail": "auto", "image_url": "data:image/png;base64,QUJD"},
			},
		},
		{
			name:    "an image a text-only model cannot take becomes a stand-in",
			model:   func(model wire.Model) wire.Model { model.Input = []string{"text"}; return model },
			content: []wire.Content{wire.ImageContent("QUJD", "image/png")},
			want:    "(see attached image)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := sseServer(t, "text.sse")
			model := testModel(server)
			if test.model != nil {
				model = test.model(model)
			}
			request := provider.Request{
				Model: model,
				Messages: wire.NormalizeContext("You are pi.", nil, []wire.Message{
					wire.UserMessage("run it"),
					wire.AssistantMessage(wire.ToolCallContent("call_1", "echo", json.RawMessage(`{}`))),
					wire.ToolResultMessage("call_1", "echo", false, test.content...),
				}),
			}
			collect(t, providerFor(server), request)

			var body struct {
				Input []struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Output any    `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(server.last(t).body, &body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			var got any
			for _, entry := range body.Input {
				if entry.Type == "function_call_output" {
					got = entry.Output
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("output: got %#v; want %#v", got, test.want)
			}
		})
	}
}

// TestReasoningSignatureStored checks what a later turn depends on: the whole
// item, encrypted text included, kept in the thinking block.
func TestReasoningSignatureStored(t *testing.T) {
	server := sseServer(t, "reasoning.sse")
	model := testModel(server)
	model.Reasoning = true
	message := collect(t, providerFor(server), textRequest(model, "hi"))

	var thinking wire.Content
	for _, block := range message.Content.Items() {
		if block.Type == wire.ContentThinking {
			thinking = block
		}
	}
	if thinking.Type != wire.ContentThinking {
		t.Fatalf("no thinking block in %+v", message.Content.Items())
	}
	if thinking.Thinking != "We need to read the file." {
		t.Fatalf("thinking: got %q", thinking.Thinking)
	}
	var stored struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal([]byte(thinking.ThinkingSignature), &stored); err != nil {
		t.Fatalf("signature is not an item: %v\n%s", err, thinking.ThinkingSignature)
	}
	if stored.Type != "reasoning" || stored.EncryptedContent != "gAAAA-reasoning-blob" {
		t.Fatalf("signature item: got %+v", stored)
	}
}

// TestErrorPaths keeps the shapes the conformance suite does not cover: an
// in-band error event and a request the server never answers with a stream.
func TestErrorPaths(t *testing.T) {
	t.Run("in-band error event", func(t *testing.T) {
		server := sseServer(t, "error.sse")
		message := collect(t, providerFor(server), textRequest(testModel(server), "hi"))
		if message.StopReason != wire.StopError {
			t.Fatalf("stop reason: got %q", message.StopReason)
		}
		if message.ErrorMessage != "responses: stream error: rate_limit_exceeded: slow down" {
			t.Fatalf("error message: got %q", message.ErrorMessage)
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := newServer(t, http.StatusUnauthorized, `{"error":{"message":"bad key"}}`)
		message := collect(t, providerFor(server), textRequest(testModel(server), "hi"))
		if message.StopReason != wire.StopError {
			t.Fatalf("stop reason: got %q", message.StopReason)
		}
		if message.ErrorMessage != "401 Unauthorized: bad key" {
			t.Fatalf("error message: got %q", message.ErrorMessage)
		}
	})

	t.Run("context overflow", func(t *testing.T) {
		server := newServer(t, http.StatusBadRequest,
			`{"error":{"message":"Your input exceeds the context window of this model","type":"invalid_request_error"}}`)
		message := collect(t, providerFor(server), textRequest(testModel(server), "hi"))
		if message.ErrorCode != wire.ErrorCodeContextOverflow {
			t.Fatalf("error code: got %q want overflow", message.ErrorCode)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		server := sseServer(t, "text.sse")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stream, err := providerFor(server).Stream(ctx, textRequest(testModel(server), "hi"))
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		message, _, err := provider.Collect(stream)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if message.StopReason != wire.StopAborted {
			t.Fatalf("stop reason: got %q; want aborted", message.StopReason)
		}
	})
}

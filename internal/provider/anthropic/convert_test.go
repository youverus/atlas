package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/wire"
)

// mustStream opens a stream and fails the test when the adapter refuses the
// request.
func mustStream(t *testing.T, p *anthropic.Provider, req provider.Request) provider.Stream {
	t.Helper()
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return stream
}

// captureRequest sends one request through a provider backed by a text stream
// and returns the body the adapter produced.
func captureRequest(t *testing.T, req provider.Request) []byte {
	t.Helper()
	server := sseServer(t, "text.sse")
	p := providerFor(server)
	if _, _, err := provider.Collect(mustStream(t, p, req)); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return server.last(t).body
}

// decodeCaptured returns the request body as a generic JSON tree.
func decodeCaptured(t *testing.T, req provider.Request) map[string]any {
	t.Helper()
	return decodeRequest(t, captureRequest(t, req))
}

// objectField returns a nested object from a decoded request.
func objectField(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := payload[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not an object: %#v", key, payload[key])
	}
	return value
}

// TestRequestGolden pins the whole request body for a transcript that exercises
// every message shape: system folding, images, thinking with a signature, tool
// calls, merged tool results, and tool declarations.
func TestRequestGolden(t *testing.T) {
	head := wire.SystemMessage("You are a test agent.")
	head.ToolsAdded = []wire.Tool{
		{
			Name:        "read",
			Description: "Read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		},
		{
			Name:        "grep",
			Description: "Search files",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}`),
		},
	}

	request := provider.Request{
		Model: wire.Model{ID: testModel, API: "anthropic-messages", Provider: "anthropic"},
		Messages: []wire.Message{
			head,
			wire.UserMessageBlocks(wire.TextContent("Look at this"), wire.ImageContent("aGk=", "image/png")),
			wire.AssistantMessage(
				wire.ThinkingContent("Need the file", "sig-1"),
				wire.TextContent("Reading."),
				wire.ToolCallContent("call_1", "read", json.RawMessage(`{"path":"a.go"}`)),
			),
			wire.ToolResultMessage("call_1", "read", false,
				wire.TextContent("package a"), wire.ImageContent("aGk=", "image/jpeg")),
			wire.ToolResultMessage("call_2", "read", true, wire.TextContent("not found")),
			wire.UserMessage("Thanks"),
		},
		Options: provider.Options{MaxTokens: wire.Ptr(8192)},
	}

	got := normalizeJSON(t, "request", captureRequest(t, request))
	want := normalizeJSON(t, "golden", readFixture(t, "request_tools.json"))
	if got != want {
		t.Fatalf("request body differs\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestSystemCollapse pins that every system message folds into one top-level
// prompt and that the messages array never carries a system role. A later
// system message extends the prompt, which is how wire.CurrentSystemMessage
// replays the transcript.
func TestSystemCollapse(t *testing.T) {
	payload := decodeCaptured(t, messageRequest(
		wire.SystemMessage("first"),
		wire.UserMessage("hi"),
		wire.SystemMessage("second"),
		wire.UserMessage("again"),
	))

	system, ok := payload["system"].([]any)
	if !ok || len(system) != 1 {
		t.Fatalf("system: %#v", payload["system"])
	}
	block, ok := system[0].(map[string]any)
	if !ok || block["text"] != "first\n\nsecond" {
		t.Fatalf("system block: %#v", system[0])
	}
	for _, message := range requestMessages(t, captureRequest(t, messageRequest(
		wire.SystemMessage("only"), wire.UserMessage("hi"),
	))) {
		if message["role"] == "system" {
			t.Fatalf("system role leaked into messages: %#v", message)
		}
	}
}

// TestEmptySystemOmitted pins that a transcript without a prompt sends no system
// field at all instead of an empty array.
func TestEmptySystemOmitted(t *testing.T) {
	payload := decodeCaptured(t, messageRequest(wire.UserMessage("hi")))
	if _, present := payload["system"]; present {
		t.Fatalf("system present without a prompt: %#v", payload["system"])
	}
	if _, present := payload["tools"]; present {
		t.Fatalf("tools present without declarations: %#v", payload["tools"])
	}
}

// TestThinkingConfig pins the level-to-budget mapping and the cases where the
// adapter drops the configuration entirely.
func TestThinkingConfig(t *testing.T) {
	reasoning := wire.Model{ID: testModel, API: "anthropic-messages", Reasoning: true, MaxTokens: 32768}

	cases := []struct {
		name    string
		model   wire.Model
		options provider.Options
		want    any // nil means no thinking field
	}{
		{name: "no level", model: reasoning, want: nil},
		{name: "off", model: reasoning, options: provider.Options{ThinkingLevel: "off"}, want: nil},
		{name: "not a reasoning model", model: wire.Model{ID: testModel}, options: provider.Options{ThinkingLevel: "high"}, want: nil},
		{name: "low", model: reasoning, options: provider.Options{ThinkingLevel: "low"}, want: 2048.0},
		{name: "medium", model: reasoning, options: provider.Options{ThinkingLevel: "medium"}, want: 8192.0},
		{name: "high", model: reasoning, options: provider.Options{ThinkingLevel: "high"}, want: 16384.0},
		{name: "xhigh clamps to high", model: reasoning, options: provider.Options{ThinkingLevel: "xhigh"}, want: 16384.0},
		{name: "max clamps to high", model: reasoning, options: provider.Options{ThinkingLevel: "max"}, want: 16384.0},
		{name: "ultra clamps to high", model: reasoning, options: provider.Options{ThinkingLevel: "ultra"}, want: 16384.0},
		{name: "unknown level clamps to high", model: reasoning, options: provider.Options{ThinkingLevel: "bogus"}, want: 16384.0},
		{
			name:    "budget leaves room for the answer",
			model:   reasoning,
			options: provider.Options{ThinkingLevel: "high", MaxTokens: wire.Ptr(6000)},
			want:    4976.0,
		},
		{
			name:    "ceiling below the minimum drops thinking",
			model:   reasoning,
			options: provider.Options{ThinkingLevel: "high", MaxTokens: wire.Ptr(1500)},
			want:    nil,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := provider.Request{Model: test.model, Messages: []wire.Message{wire.UserMessage("hi")}, Options: test.options}
			payload := decodeCaptured(t, request)
			raw, present := payload["thinking"]
			if test.want == nil {
				if present {
					t.Fatalf("thinking present: %#v", raw)
				}
				return
			}
			thinking := objectField(t, payload, "thinking")
			if thinking["type"] != "enabled" {
				t.Fatalf("thinking type: %#v", thinking["type"])
			}
			if thinking["budget_tokens"] != test.want {
				t.Fatalf("budget: got %#v want %v", thinking["budget_tokens"], test.want)
			}
		})
	}
}

// TestTemperatureOmittedWithThinking pins that temperature travels alone: the
// API rejects it together with a thinking budget.
func TestTemperatureOmittedWithThinking(t *testing.T) {
	reasoning := wire.Model{ID: testModel, Reasoning: true, MaxTokens: 32768}
	request := provider.Request{
		Model:    reasoning,
		Messages: []wire.Message{wire.UserMessage("hi")},
		Options:  provider.Options{Temperature: wire.Ptr(0.4), ThinkingLevel: "low"},
	}
	if _, present := decodeCaptured(t, request)["temperature"]; present {
		t.Fatal("temperature sent together with thinking")
	}

	request.Options.ThinkingLevel = "off"
	temperature, present := decodeCaptured(t, request)["temperature"]
	if !present || temperature != 0.4 {
		t.Fatalf("temperature: present=%v value=%#v", present, temperature)
	}
}

// TestMaxTokensPriority pins the output limit: an explicit option wins over the
// model declaration, which wins over the default the API requires.
func TestMaxTokensPriority(t *testing.T) {
	cases := []struct {
		name    string
		model   wire.Model
		options provider.Options
		want    float64
	}{
		{
			name:    "option wins",
			model:   wire.Model{ID: testModel, MaxTokens: 4096},
			options: provider.Options{MaxTokens: wire.Ptr(512)},
			want:    512,
		},
		{
			name:    "zero option is ignored",
			model:   wire.Model{ID: testModel, MaxTokens: 4096},
			options: provider.Options{MaxTokens: wire.Ptr(0)},
			want:    4096,
		},
		{
			name:  "model wins",
			model: wire.Model{ID: testModel, MaxTokens: 32000},
			want:  32000,
		},
		{
			name:  "default covers a catalog-less model",
			model: wire.Model{ID: testModel},
			want:  4096,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := provider.Request{Model: test.model, Messages: []wire.Message{wire.UserMessage("hi")}, Options: test.options}
			if got := decodeCaptured(t, request)["max_tokens"]; got != test.want {
				t.Fatalf("max_tokens: got %#v want %v", got, test.want)
			}
		})
	}
}

// TestToolSchemaDefault pins that a tool without parameters still carries a
// schema object, because a null input_schema is rejected.
func TestToolSchemaDefault(t *testing.T) {
	head := wire.SystemMessage("prompt")
	head.ToolsAdded = []wire.Tool{
		{Name: "ping", Description: "No arguments"},
		{Name: "read", Description: "Read", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
	}
	payload := decodeCaptured(t, messageRequest(head, wire.UserMessage("hi")))

	raw, ok := payload["tools"].([]any)
	if !ok || len(raw) != 2 {
		t.Fatalf("tools: %#v", payload["tools"])
	}
	first, ok := raw[0].(map[string]any)
	if !ok {
		t.Fatalf("tool is not an object: %#v", raw[0])
	}
	schema, ok := first["input_schema"].(map[string]any)
	if !ok {
		t.Fatalf("input_schema: %#v", first["input_schema"])
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type: %#v", schema["type"])
	}
	// The API requires properties to be an object, not null.
	if properties, ok := schema["properties"].(map[string]any); !ok || len(properties) != 0 {
		t.Fatalf("schema properties: %#v", schema["properties"])
	}
}

// TestToolSchemaPassthrough pins that a declared schema is forwarded unchanged,
// not re-encoded field by field.
func TestToolSchemaPassthrough(t *testing.T) {
	head := wire.SystemMessage("prompt")
	head.ToolsAdded = []wire.Tool{{
		Name:        "read",
		Description: "Read",
		Parameters:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"path":{"type":"string"}}}`),
	}}
	tools, ok := decodeCaptured(t, messageRequest(head, wire.UserMessage("hi")))["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: %#v", tools)
	}
	tool, _ := tools[0].(map[string]any)
	schema := objectField(t, tool, "input_schema")
	if schema["additionalProperties"] != false {
		t.Fatalf("schema lost a field: %#v", schema)
	}
}

// TestPlainUserContentStaysString pins that a text-only turn is sent as a string
// rather than a one-element block array, which is the shape the API documents.
func TestPlainUserContentStaysString(t *testing.T) {
	messages := requestMessages(t, captureRequest(t, messageRequest(wire.UserMessage("hello"))))
	if len(messages) != 1 {
		t.Fatalf("messages: %#v", messages)
	}
	if content, ok := messages[0]["content"].(string); !ok || content != "hello" {
		t.Fatalf("content: %#v", messages[0]["content"])
	}
}

// TestBlankContentDropped pins that whitespace-only turns produce no message
// instead of an empty content the API rejects.
func TestBlankContentDropped(t *testing.T) {
	payload := messageRequest(
		wire.UserMessage("   "),
		wire.AssistantMessage(wire.TextContent("")),
		wire.UserMessage("real"),
	)
	messages := requestMessages(t, captureRequest(t, payload))
	if len(messages) != 1 {
		t.Fatalf("messages: %#v", messages)
	}
	if content, _ := messages[0]["content"].(string); content != "real" {
		t.Fatalf("content: %#v", messages[0]["content"])
	}
}

// TestThinkingWithoutSignatureBecomesText pins the downgrade for a thinking
// block that cannot be replayed: the API requires a signature, so the text goes
// back as plain text instead of a block the API rejects.
func TestThinkingWithoutSignatureBecomesText(t *testing.T) {
	messages := requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessage("hi"),
		wire.AssistantMessage(wire.ThinkingContent("half a thought", "")),
		wire.UserMessage("again"),
	)))
	if len(messages) != 3 {
		t.Fatalf("messages: %#v", messages)
	}
	blocks, ok := messages[1]["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("assistant content: %#v", messages[1]["content"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "half a thought" {
		t.Fatalf("block: %#v", block)
	}
	if _, present := block["signature"]; present {
		t.Fatalf("downgraded block kept a signature: %#v", block)
	}
}

// TestRedactedThinkingReplay pins that a withheld reasoning block travels back
// as the same block type carrying its opaque payload, and that an empty payload
// is dropped rather than sent as an empty string.
func TestRedactedThinkingReplay(t *testing.T) {
	redacted := wire.ThinkingContent("[Reasoning redacted]", "sealed-blob")
	redacted.Redacted = wire.Ptr(true)

	messages := requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessage("hi"),
		wire.AssistantMessage(redacted, wire.TextContent("done")),
		wire.UserMessage("ok"),
	)))
	blocks, ok := messages[1]["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("assistant content: %#v", messages[1]["content"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "redacted_thinking" || block["data"] != "sealed-blob" {
		t.Fatalf("redacted block: %#v", block)
	}

	empty := wire.ThinkingContent("[Reasoning redacted]", "")
	empty.Redacted = wire.Ptr(true)
	messages = requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessage("hi"),
		wire.AssistantMessage(empty, wire.TextContent("done")),
		wire.UserMessage("ok"),
	)))
	blocks, _ = messages[1]["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("empty redacted block kept: %#v", blocks)
	}
}

// TestToolCallWithoutArguments pins that a call without arguments still sends an
// object, since the API requires one.
func TestToolCallWithoutArguments(t *testing.T) {
	messages := requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessage("hi"),
		wire.AssistantMessage(wire.ToolCallContent("call_1", "ping", nil)),
		wire.ToolResultMessage("call_1", "ping", false, wire.TextContent("ok")),
	)))
	blocks, ok := messages[1]["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("assistant content: %#v", messages[1]["content"])
	}
	block, _ := blocks[0].(map[string]any)
	input, ok := block["input"].(map[string]any)
	if !ok || len(input) != 0 {
		t.Fatalf("input: %#v", block["input"])
	}
}

// TestToolResultMerging pins that a run of tool results travels in one user
// message, in order, and that the run ends where the next role changes.
func TestToolResultMerging(t *testing.T) {
	messages := requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessage("hi"),
		wire.AssistantMessage(
			wire.ToolCallContent("call_1", "read", json.RawMessage(`{}`)),
			wire.ToolCallContent("call_2", "read", json.RawMessage(`{}`)),
		),
		wire.ToolResultMessage("call_1", "read", false, wire.TextContent("a")),
		wire.ToolResultMessage("call_2", "read", false, wire.TextContent("b")),
		wire.UserMessage("next"),
	)))

	if len(messages) != 4 {
		t.Fatalf("message count: got %d, want 4: %#v", len(messages), messages)
	}
	if role, _ := messages[2]["role"].(string); role != "user" {
		t.Fatalf("tool results role: %#v", messages[2]["role"])
	}
	blocks, ok := messages[2]["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("tool results: %#v", messages[2]["content"])
	}
	for i, want := range []string{"call_1", "call_2"} {
		block, _ := blocks[i].(map[string]any)
		if block["type"] != "tool_result" || block["tool_use_id"] != want {
			t.Fatalf("block %d: %#v", i, block)
		}
	}
	if _, present := messages[3]["content"].(string); !present {
		t.Fatalf("following user turn: %#v", messages[3]["content"])
	}
}

// TestImageOnlyToolResultGetsPlaceholder pins the text block the API requires
// when a tool result carries images and no text.
func TestImageOnlyToolResultGetsPlaceholder(t *testing.T) {
	messages := requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessage("hi"),
		wire.AssistantMessage(wire.ToolCallContent("call_1", "shot", json.RawMessage(`{}`))),
		wire.ToolResultMessage("call_1", "shot", false, wire.ImageContent("aGk=", "image/png")),
	)))
	blocks, ok := messages[2]["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("tool result content: %#v", messages[2]["content"])
	}
	result, _ := blocks[0].(map[string]any)
	inner, ok := result["content"].([]any)
	if !ok || len(inner) != 2 {
		t.Fatalf("result content: %#v", result["content"])
	}
	text, _ := inner[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "(see attached image)" {
		t.Fatalf("placeholder: %#v", text)
	}
	image, _ := inner[1].(map[string]any)
	source, ok := image["source"].(map[string]any)
	if !ok || source["media_type"] != "image/png" || source["type"] != "base64" {
		t.Fatalf("image source: %#v", image["source"])
	}
}

// TestConversionErrors pins the messages the adapter refuses to send. Each one
// would be a request the API rejects, so it surfaces as a local error.
func TestConversionErrors(t *testing.T) {
	cases := []struct {
		name     string
		messages []wire.Message
		want     string
	}{
		{
			name: "tool result without a call id",
			messages: []wire.Message{
				wire.UserMessage("hi"),
				wire.ToolResultMessage("", "read", false, wire.TextContent("x")),
			},
			want: "toolCallId",
		},
		{
			name: "tool call without an id",
			messages: []wire.Message{
				wire.UserMessage("hi"),
				wire.AssistantMessage(wire.ToolCallContent("", "read", json.RawMessage(`{}`))),
			},
			want: "has no id",
		},
		{
			name: "image without a media type",
			messages: []wire.Message{
				wire.UserMessageBlocks(wire.ImageContent("aGk=", "")),
			},
			want: "mimeType",
		},
		{
			name: "image in a tool result without a media type",
			messages: []wire.Message{
				wire.UserMessage("hi"),
				wire.ToolResultMessage("call_1", "shot", false, wire.ImageContent("aGk=", "")),
			},
			want: "mimeType",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := sseServer(t, "text.sse")
			p := providerFor(server)
			_, err := p.Stream(t.Context(), provider.Request{Model: wire.Model{ID: testModel}, Messages: test.messages})
			if err == nil {
				t.Fatalf("stream accepted a request it cannot represent")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: got %q, want it to mention %q", err, test.want)
			}
			if len(server.captured) != 0 {
				t.Fatalf("a broken request reached the server: %s", server.captured[0].body)
			}
		})
	}
}

// TestImageWithoutDataDropped pins that an image block with no payload is
// skipped instead of sent as an empty base64 string.
func TestImageWithoutDataDropped(t *testing.T) {
	messages := requestMessages(t, captureRequest(t, messageRequest(
		wire.UserMessageBlocks(wire.TextContent("look"), wire.ImageContent("", "image/png")),
	)))
	blocks, ok := messages[0]["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content: %#v", messages[0]["content"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("block: %#v", block)
	}
}

// TestUnknownRoleRejected pins that a role the API has no slot for fails instead
// of being sent as a user turn.
func TestUnknownRoleRejected(t *testing.T) {
	server := sseServer(t, "text.sse")
	p := providerFor(server)
	_, err := p.Stream(t.Context(), provider.Request{
		Model:    wire.Model{ID: testModel},
		Messages: []wire.Message{{Role: wire.Role("orchestrator"), Content: wire.NewTextList("hi")}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported message role") {
		t.Fatalf("error: %v", err)
	}
	if len(server.captured) != 0 {
		t.Fatal("an unsupported role reached the server")
	}
}

// TestHeaderOverrideReplacesDefault pins that an Options header wins over the
// adapter's default, which is how a caller swaps the API version.
func TestHeaderOverrideReplacesDefault(t *testing.T) {
	server := sseServer(t, "text.sse")
	p := providerFor(server)
	request := messageRequest(wire.UserMessage("hi"))
	request.Options.Headers = map[string]string{"anthropic-version": "2099-01-01"}
	if _, err := p.Stream(t.Context(), request); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := server.last(t).header.Values("anthropic-version"); !reflect.DeepEqual(got, []string{"2099-01-01"}) {
		t.Fatalf("anthropic-version: %#v", got)
	}
	if got := server.last(t).header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type: %q", got)
	}
}

// TestHTTPStatusWithoutBody pins that an error response with no body still
// reports the status instead of an empty message.
func TestHTTPStatusWithoutBody(t *testing.T) {
	server := newServer(t, http.StatusInternalServerError, "")
	p := providerFor(server)
	message, events, err := provider.Collect(mustStream(t, p, messageRequest(wire.UserMessage("hi"))))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Type != wire.EventError {
		t.Fatalf("events: %v", eventTypes(events))
	}
	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: %q", message.StopReason)
	}
	if !strings.Contains(message.ErrorMessage, "500") {
		t.Fatalf("error message: %q", message.ErrorMessage)
	}
}

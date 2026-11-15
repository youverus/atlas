package google_test

import (
	"encoding/json"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/google"
	"pi-go/internal/wire"
)

// captureWith sends one request through a provider built from compat and
// returns the decoded body that reached the server.
func captureWith(t *testing.T, compat google.Compat, request provider.Request) map[string]any {
	t.Helper()
	server := sseServer(t, "text.sse")
	compat.BaseURL = server.URL
	request.Model.BaseURL = ""

	p := google.New(compat)
	stream, err := p.Stream(t.Context(), request)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, _, err := provider.Collect(stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return decodeBody(t, server.last(t).body)
}

// thinkingRequest is the smallest request that reaches the thinking fields: one
// user turn and the level under test.
func thinkingRequest(level string) provider.Request {
	return provider.Request{
		Model:    reasoningModel(),
		Messages: []wire.Message{wire.UserMessage("hi")},
		Options:  provider.Options{ThinkingLevel: level},
	}
}

// TestThinkingDialects pins which dial a model family gets. The level family
// takes a gear, everything else a token allowance, and the compat value
// overrides the guess because a gateway may rename a model.
func TestThinkingDialects(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		compat string
		level  string
		want   map[string]any // nil means no thinkingConfig
	}{
		{
			name:  "gemini 3 pro takes a level",
			model: "gemini-3-pro-preview",
			level: "high",
			want:  map[string]any{"includeThoughts": true, "thinkingLevel": "HIGH"},
		},
		{
			name:  "a minor version still takes a level",
			model: "gemini-3.1-flash",
			level: "low",
			want:  map[string]any{"includeThoughts": true, "thinkingLevel": "LOW"},
		},
		{
			name:  "the rolling alias takes a level",
			model: "gemini-flash-latest",
			level: "medium",
			want:  map[string]any{"includeThoughts": true, "thinkingLevel": "MEDIUM"},
		},
		{
			name:  "gemma 4 takes a level",
			model: "gemma-4-27b",
			level: "high",
			want:  map[string]any{"includeThoughts": true, "thinkingLevel": "HIGH"},
		},
		{
			// A gear above high stays at high: this dial has no tier above it.
			name:  "max clamps to high",
			model: "gemini-3-pro",
			level: "max",
			want:  map[string]any{"includeThoughts": true, "thinkingLevel": "HIGH"},
		},
		{
			name:  "older gemini takes a budget",
			model: "gemini-2.5-pro",
			level: "low",
			want:  map[string]any{"includeThoughts": true, "thinkingBudget": float64(2048)},
		},
		{
			name:  "the budget is capped where flash stops",
			model: "gemini-2.5-pro",
			level: "max",
			want:  map[string]any{"includeThoughts": true, "thinkingBudget": float64(24576)},
		},
		{
			// Off is a zero budget here: the model is told to spend nothing.
			name:  "off is an empty budget",
			model: "gemini-3-pro",
			level: "off",
			want:  map[string]any{"thinkingBudget": float64(0)},
		},
		{
			name:  "no level leaves the vendor default alone",
			model: "gemini-3-pro",
			level: "",
		},
		{
			name:   "a compat value wins over the id",
			model:  "gemini-2.5-pro",
			compat: google.DialectLevel,
			level:  "high",
			want:   map[string]any{"includeThoughts": true, "thinkingLevel": "HIGH"},
		},
		{
			name:   "the other compat value also wins",
			model:  "gemini-3-pro",
			compat: google.DialectBudget,
			level:  "low",
			want:   map[string]any{"includeThoughts": true, "thinkingBudget": float64(2048)},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := thinkingRequest(test.level)
			request.Model.ID = test.model
			compat := google.Compat{ThinkingDialect: test.compat}
			payload := captureWith(t, compat, request)

			generation, present := payload["generationConfig"].(map[string]any)
			if !present {
				t.Fatalf("generationConfig missing: %#v", payload)
			}
			got, present := generation["thinkingConfig"]
			if test.want == nil {
				if present {
					t.Fatalf("thinkingConfig present: %#v", got)
				}
				return
			}
			settings, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("thinkingConfig is not an object: %#v", got)
			}
			if len(settings) != len(test.want) {
				t.Fatalf("thinkingConfig: got %#v want %#v", settings, test.want)
			}
			for key, want := range test.want {
				if settings[key] != want {
					t.Fatalf("thinkingConfig.%s: got %#v want %#v", key, settings[key], want)
				}
			}
		})
	}
}

// TestThinkingNeedsTheModelToDeclareReasoning keeps the configuration off a
// model whose declaration says it does not think.
func TestThinkingNeedsTheModelToDeclareReasoning(t *testing.T) {
	request := thinkingRequest("high")
	request.Model.Reasoning = false
	payload := captureWith(t, google.Compat{}, request)

	generation := objectField(t, payload, "generationConfig")
	if got, present := generation["thinkingConfig"]; present {
		t.Fatalf("thinkingConfig present: %#v", got)
	}
}

// TestTemperatureTravelsWithThinking pins the one place this API differs from
// the other two: it accepts a temperature next to its thinking fields.
func TestTemperatureTravelsWithThinking(t *testing.T) {
	request := thinkingRequest("high")
	request.Options.Temperature = wire.Ptr(0.3)
	payload := captureWith(t, google.Compat{}, request)

	generation := objectField(t, payload, "generationConfig")
	if got := generation["temperature"]; got != 0.3 {
		t.Fatalf("temperature: got %#v want 0.3", got)
	}
	if _, present := generation["thinkingConfig"]; !present {
		t.Fatalf("thinkingConfig missing: %#v", generation)
	}
}

// TestSystemPromptTravelsBesideTheConversation pins that this API has no system
// role: the prompt is its own field and no turn carries it.
func TestSystemPromptTravelsBesideTheConversation(t *testing.T) {
	request := provider.Request{
		Model: reasoningModel(),
		Messages: wire.NormalizeContext("You are a test agent.", nil, []wire.Message{
			wire.UserMessage("hi"),
		}),
	}
	payload := captureWith(t, google.Compat{}, request)

	instruction := objectField(t, payload, "systemInstruction")
	parts, ok := instruction["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("systemInstruction.parts: got %#v", instruction["parts"])
	}
	if text := parts[0].(map[string]any)["text"]; text != "You are a test agent." {
		t.Fatalf("system prompt: got %#v", text)
	}

	turns, ok := payload["contents"].([]any)
	if !ok || len(turns) != 1 {
		t.Fatalf("contents: got %#v want one turn", payload["contents"])
	}
	if role := turns[0].(map[string]any)["role"]; role != "user" {
		t.Fatalf("first turn role: got %#v want user", role)
	}
}

// TestToolChoiceTravels pins the shared vocabulary translated into the modes
// this API documents, where requiring a call is spelled ANY.
func TestToolChoiceTravels(t *testing.T) {
	cases := []struct {
		value string
		want  string // empty means no toolConfig
	}{
		{value: ""},
		{value: "auto", want: "AUTO"},
		{value: "none", want: "NONE"},
		{value: "required", want: "ANY"},
		{value: "bogus"},
	}

	for _, test := range cases {
		t.Run("value="+test.value, func(t *testing.T) {
			compat := google.Compat{ToolChoice: test.value}
			payload := captureWith(t, compat, toolRequest(readTool()))

			got, present := payload["toolConfig"]
			if test.want == "" {
				if present {
					t.Fatalf("toolConfig present: %#v", got)
				}
				return
			}
			call := objectField(t, objectField(t, payload, "toolConfig"), "functionCallingConfig")
			if call["mode"] != test.want {
				t.Fatalf("mode: got %#v want %q", call["mode"], test.want)
			}
		})
	}
}

// toolRequest declares one tool so the declaration and tool_choice paths have
// something to point at.
func toolRequest(tools ...wire.Tool) provider.Request {
	head := wire.SystemMessage("You are a test agent.")
	head.ToolsAdded = tools
	return provider.Request{
		Model:    reasoningModel(),
		Messages: []wire.Message{head, wire.UserMessage("hi")},
	}
}

func readTool() wire.Tool {
	return wire.Tool{
		Name:        "read",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}
}

// TestToolDeclarationShape pins the field this API takes the schema in, and that
// an empty schema is filled rather than sent as null.
func TestToolDeclarationShape(t *testing.T) {
	payload := captureWith(t, google.Compat{}, toolRequest(readTool(), wire.Tool{Name: "bare", Description: "No schema"}))

	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: got %#v", payload["tools"])
	}
	set := tools[0].(map[string]any)
	declarations, ok := set["functionDeclarations"].([]any)
	if !ok || len(declarations) != 2 {
		t.Fatalf("functionDeclarations: got %#v", set["functionDeclarations"])
	}

	read := declarations[0].(map[string]any)
	parameters, ok := read["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters: got %#v", read["parameters"])
	}
	if parameters["type"] != "object" {
		t.Fatalf("parameters.type: got %#v", parameters["type"])
	}
	if _, present := read["parametersJsonSchema"]; present {
		t.Fatal("the newer schema field travelled; this adapter uses the OpenAPI subset")
	}

	bare := declarations[1].(map[string]any)
	bareParameters, ok := bare["parameters"].(map[string]any)
	if !ok || bareParameters["type"] != "object" {
		t.Fatalf("empty schema not filled: %#v", bare["parameters"])
	}
}

// TestToolResultsShareOneTurn pins that a run of results becomes one user turn,
// because each function response answers the model turn before it, and that
// failure travels under the error key.
func TestToolResultsShareOneTurn(t *testing.T) {
	head := wire.SystemMessage("You are a test agent.")
	head.ToolsAdded = []wire.Tool{readTool()}
	request := provider.Request{
		Model: reasoningModel(),
		Messages: []wire.Message{
			head,
			wire.UserMessage("read two files"),
			wire.AssistantMessage(
				wire.ToolCallContent("call_a", "read", json.RawMessage(`{"path":"a.go"}`)),
				wire.ToolCallContent("call_b", "read", json.RawMessage(`{"path":"b.go"}`)),
			),
			wire.ToolResultMessage("call_a", "read", false, wire.TextContent("package a")),
			wire.ToolResultMessage("call_b", "read", true, wire.TextContent("not found")),
		},
	}
	payload := captureWith(t, google.Compat{}, request)

	turns, ok := payload["contents"].([]any)
	if !ok || len(turns) != 3 {
		t.Fatalf("contents: got %#v want three turns", payload["contents"])
	}

	model := turns[1].(map[string]any)
	if role := model["role"]; role != "model" {
		t.Fatalf("assistant role: got %#v want model", role)
	}
	calls, ok := model["parts"].([]any)
	if !ok || len(calls) != 2 {
		t.Fatalf("model parts: got %#v", model["parts"])
	}
	if id := calls[0].(map[string]any)["functionCall"].(map[string]any)["id"]; id != "call_a" {
		t.Fatalf("first call id: got %#v", id)
	}

	results := turns[2].(map[string]any)
	if role := results["role"]; role != "user" {
		t.Fatalf("tool result role: got %#v want user", role)
	}
	parts, ok := results["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("tool result parts: got %#v", results["parts"])
	}
	first := parts[0].(map[string]any)["functionResponse"].(map[string]any)
	if output := first["response"].(map[string]any)["output"]; output != "package a" {
		t.Fatalf("first response: got %#v", first["response"])
	}
	if id := first["id"]; id != "call_a" {
		t.Fatalf("first response id: got %#v", id)
	}
	second := parts[1].(map[string]any)["functionResponse"].(map[string]any)
	if message := second["response"].(map[string]any)["error"]; message != "not found" {
		t.Fatalf("second response: got %#v", second["response"])
	}
}

// TestToolCallIdsAreSanitized pins the character set this API accepts: an id
// from another provider can carry a dot or a colon, and the same value has to go
// back on the response.
func TestToolCallIdsAreSanitized(t *testing.T) {
	head := wire.SystemMessage("You are a test agent.")
	head.ToolsAdded = []wire.Tool{readTool()}
	request := provider.Request{
		Model: reasoningModel(),
		Messages: []wire.Message{
			head,
			wire.UserMessage("read"),
			wire.AssistantMessage(wire.ToolCallContent("call.with:odd/id", "read", json.RawMessage(`{}`))),
			wire.ToolResultMessage("call.with:odd/id", "read", false, wire.TextContent("ok")),
		},
	}
	payload := captureWith(t, google.Compat{}, request)

	turns := payload["contents"].([]any)
	call := turns[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	response := turns[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if call["id"] != "call_with_odd_id" {
		t.Fatalf("call id: got %#v", call["id"])
	}
	if call["id"] != response["id"] {
		t.Fatalf("call id %#v does not match response id %#v", call["id"], response["id"])
	}
}

// TestToolResultWithoutCallIdFails pins the one message this adapter refuses to
// guess about: a result nobody can pair with a call.
func TestToolResultWithoutCallIdFails(t *testing.T) {
	request := provider.Request{
		Model:    reasoningModel(),
		Messages: []wire.Message{wire.UserMessage("hi"), wire.ToolResultMessage("", "read", false, wire.TextContent("x"))},
	}
	p := google.New(google.Compat{})
	if _, err := p.Stream(t.Context(), request); err == nil {
		t.Fatal("a tool result without a call id was accepted")
	}
}

// TestThinkingTextIsReplayed pins the round trip: a thought signature only
// survives when it came from this provider and model and looks like the base64
// blob the API issued.
func TestThinkingTextIsReplayed(t *testing.T) {
	head := wire.SystemMessage("You are a test agent.")
	// A text block carries its signature in its own field, so the block is
	// built by hand: the constructor takes text alone.
	signed := wire.TextContent("answer")
	signed.TextSignature = "c2lnLWI="
	assistant := wire.AssistantMessage(
		wire.ThinkingContent("weighing", "c2lnLWE="),
		signed,
	)
	assistant.Provider = "google"
	assistant.Model = testModel

	request := provider.Request{
		Model:    reasoningModel(),
		Messages: []wire.Message{head, wire.UserMessage("hi"), assistant, wire.UserMessage("again")},
	}
	payload := captureWith(t, google.Compat{}, request)

	turns := payload["contents"].([]any)
	parts := turns[1].(map[string]any)["parts"].([]any)
	thinking := parts[0].(map[string]any)
	if thinking["thought"] != true {
		t.Fatalf("thinking part lost its flag: %#v", thinking)
	}
	if thinking["thoughtSignature"] != "c2lnLWE=" {
		t.Fatalf("thinking signature: got %#v", thinking["thoughtSignature"])
	}
	text := parts[1].(map[string]any)
	if text["thoughtSignature"] != "c2lnLWI=" {
		t.Fatalf("text signature: got %#v", text["thoughtSignature"])
	}
}

// TestForeignSignatureIsDropped pins the other side: a signature from another
// model means nothing here, and this API rejects one it did not issue.
func TestForeignSignatureIsDropped(t *testing.T) {
	head := wire.SystemMessage("You are a test agent.")
	assistant := wire.AssistantMessage(
		wire.ThinkingContent("weighing", "c2lnLWE="),
		wire.TextContent("answer"),
	)
	assistant.Provider = "anthropic"
	assistant.Model = "claude-x"

	request := provider.Request{
		Model:    reasoningModel(),
		Messages: []wire.Message{head, wire.UserMessage("hi"), assistant, wire.UserMessage("again")},
	}
	payload := captureWith(t, google.Compat{}, request)

	turns := payload["contents"].([]any)
	for _, entry := range turns[1].(map[string]any)["parts"].([]any) {
		if _, present := entry.(map[string]any)["thoughtSignature"]; present {
			t.Fatalf("a foreign signature travelled: %#v", entry)
		}
	}
}

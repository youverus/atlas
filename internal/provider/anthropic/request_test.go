package anthropic_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/wire"
)

// adaptiveRequest is the smallest request that reaches the adaptive thinking
// fields: one user turn, a temperature that must not travel with thinking, and
// the level under test. The model declares reasoning, because a level means
// nothing to a model that says it does not think.
func adaptiveRequest(level string) provider.Request {
	return provider.Request{
		Model: wire.Model{
			ID:        "claude-sonnet-5",
			API:       anthropic.APIName,
			Provider:  "anthropic",
			Reasoning: true,
			MaxTokens: 32768,
		},
		Messages: []wire.Message{wire.UserMessage("hi")},
		Options:  provider.Options{Temperature: wire.Ptr(0.4), ThinkingLevel: level},
	}
}

// TestAdaptiveThinkingGolden pins the four states adaptive thinking can be in as
// whole request bodies: the vendor default for an unset level, the documented
// toggle for off, and the type plus an effort dial above it. The effort dial
// shares output_config with the DeepSeek path, which is why the two shapes
// cannot travel together.
func TestAdaptiveThinkingGolden(t *testing.T) {
	const head = `"model": "claude-sonnet-5",
		"max_tokens": 32768,
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true`

	cases := []struct {
		name  string
		level string
		want  string
	}{
		{
			// An empty level asks for nothing, so the deployment's default
			// stands and a temperature is still legal.
			name: "no level leaves the vendor default alone",
			want: `{` + head + `, "temperature": 0.4}`,
		},
		{
			// Off is a value this API documents, and thinking is off again, so
			// the temperature travels.
			name:  "off disables the mode",
			level: "off",
			want:  `{` + head + `, "thinking": {"type": "disabled"}, "temperature": 0.4}`,
		},
		{
			name:  "low",
			level: "low",
			want: `{` + head + `, "thinking": {"type": "adaptive", "display": "summarized"},
				"output_config": {"effort": "low"}}`,
		},
		{
			// A gear beyond the three this API names stays at high rather than
			// inventing a tier the model may not have.
			name:  "max",
			level: "max",
			want: `{` + head + `, "thinking": {"type": "adaptive", "display": "summarized"},
				"output_config": {"effort": "high"}}`,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			compat := anthropic.Compat{AdaptiveThinking: true}
			goldenBody(t, captureWith(t, compat, adaptiveRequest(test.level)), test.want)
		})
	}
}

// TestAdaptiveEffortTable pins the whole level table, including the two levels
// the API cannot tell apart from high.
func TestAdaptiveEffortTable(t *testing.T) {
	cases := []struct {
		level  string
		toggle string // empty means no thinking field
		effort string // empty means no output_config field
	}{
		{level: ""},
		{level: "off", toggle: "disabled"},
		{level: "low", toggle: "adaptive", effort: "low"},
		{level: "medium", toggle: "adaptive", effort: "medium"},
		{level: "high", toggle: "adaptive", effort: "high"},
		{level: "xhigh", toggle: "adaptive", effort: "high"},
		{level: "max", toggle: "adaptive", effort: "high"},
		{level: "ultra", toggle: "adaptive", effort: "high"},
		// An unknown level must not read as "say nothing": the caller asked for
		// thinking, and high is the vendor default.
		{level: "bogus", toggle: "adaptive", effort: "high"},
	}

	for _, test := range cases {
		t.Run("level="+test.level, func(t *testing.T) {
			compat := anthropic.Compat{AdaptiveThinking: true}
			payload := decodeRequest(t, captureWith(t, compat, adaptiveRequest(test.level)))

			if test.toggle == "" {
				if raw, present := payload["thinking"]; present {
					t.Fatalf("thinking present: %#v", raw)
				}
			} else if toggle := objectField(t, payload, "thinking")["type"]; toggle != test.toggle {
				t.Fatalf("thinking.type: got %#v want %q", toggle, test.toggle)
			}

			if test.effort == "" {
				if raw, present := payload["output_config"]; present {
					t.Fatalf("output_config present: %#v", raw)
				}
				return
			}
			if effort := objectField(t, payload, "output_config")["effort"]; effort != test.effort {
				t.Fatalf("effort: got %#v want %q", effort, test.effort)
			}
		})
	}
}

// TestAdaptiveThinkingNeedsTheModelToDeclareReasoning guards the one field that
// would otherwise be sent to a model that cannot think: the API rejects a
// thinking level it has no notion of.
func TestAdaptiveThinkingNeedsTheModelToDeclareReasoning(t *testing.T) {
	request := adaptiveRequest("high")
	request.Model.Reasoning = false

	payload := decodeRequest(t, captureWith(t, anthropic.Compat{AdaptiveThinking: true}, request))
	if raw, present := payload["thinking"]; present {
		t.Fatalf("thinking present: %#v", raw)
	}
	if payload["temperature"] != 0.4 {
		t.Fatalf("temperature: got %#v, want the request value to travel", payload["temperature"])
	}
}

// TestAdaptiveThinkingIsOptIn pins the default: a compat table without the flag
// keeps sending the token budget, because a model that predates the adaptive
// form rejects the type outright.
func TestAdaptiveThinkingIsOptIn(t *testing.T) {
	payload := decodeRequest(t, captureWith(t, anthropic.Compat{}, adaptiveRequest("high")))
	thinking := objectField(t, payload, "thinking")
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type: got %#v want enabled", thinking["type"])
	}
	if _, present := thinking["budget_tokens"]; !present {
		t.Fatalf("budget_tokens missing: %#v", thinking)
	}
	if raw, present := payload["output_config"]; present {
		t.Fatalf("output_config present: %#v", raw)
	}
}

// toolRequest declares one tool so the tool_choice and strict paths have
// something to point at.
func toolRequest(tools ...wire.Tool) provider.Request {
	head := wire.SystemMessage("You are a test agent.")
	head.ToolsAdded = tools
	return provider.Request{
		Model:    wire.Model{ID: "claude-sonnet-5", API: anthropic.APIName, Provider: "anthropic"},
		Messages: []wire.Message{head, wire.UserMessage("hi")},
	}
}

// readTool is a schema strict mode accepts in full: an object whose only
// property is a string.
func readTool(strict bool) wire.Tool {
	return wire.Tool{
		Name:        "read",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		Strict:      strict,
	}
}

// TestToolChoiceTravels pins the three-value vocabulary and its translation:
// requiring a call is spelled "any" here. An unknown value sends nothing rather
// than a string the API rejects.
func TestToolChoiceTravels(t *testing.T) {
	cases := []struct {
		value string
		want  string // empty means no tool_choice field
	}{
		{value: ""},
		{value: "auto", want: "auto"},
		{value: "none", want: "none"},
		{value: "required", want: "any"},
		{value: "bogus"},
	}

	for _, test := range cases {
		t.Run("value="+test.value, func(t *testing.T) {
			compat := anthropic.Compat{ToolChoice: test.value}
			payload := decodeRequest(t, captureWith(t, compat, toolRequest(readTool(false))))

			raw, present := payload["tool_choice"]
			if test.want == "" {
				if present {
					t.Fatalf("tool_choice present: %#v", raw)
				}
				return
			}
			if choice := objectField(t, payload, "tool_choice")["type"]; choice != test.want {
				t.Fatalf("tool_choice.type: got %#v want %q", choice, test.want)
			}
		})
	}
}

// TestToolChoiceNeedsTools pins that the field never travels alone: a request
// without declarations has nothing to choose from, and the API rejects the
// combination.
func TestToolChoiceNeedsTools(t *testing.T) {
	compat := anthropic.Compat{ToolChoice: "required"}
	payload := decodeRequest(t, captureWith(t, compat, messageRequest(wire.UserMessage("hi"))))

	if raw, present := payload["tool_choice"]; present {
		t.Fatalf("tool_choice present without tools: %#v", raw)
	}
}

// TestStrictToolsFollowTwoDeclarations pins the gate: the compat flag says the
// endpoint supports constrained decoding and the tool says it wants it. Either
// one alone stays loose, and slipping through would cost the request.
func TestStrictToolsFollowTwoDeclarations(t *testing.T) {
	cases := []struct {
		name   string
		compat bool
		tool   bool
		want   bool
	}{
		{name: "neither", compat: false, tool: false, want: false},
		{name: "tool alone", compat: false, tool: true, want: false},
		{name: "endpoint alone", compat: true, tool: false, want: false},
		{name: "both", compat: true, tool: true, want: true},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			compat := anthropic.Compat{StrictTools: test.compat}
			payload := decodeRequest(t, captureWith(t, compat, toolRequest(readTool(test.tool))))
			def := firstTool(t, payload)

			strict, present := def["strict"]
			if !test.want {
				if present {
					t.Fatalf("strict present: %#v", strict)
				}
				return
			}
			if strict != true {
				t.Fatalf("strict: got %#v want true", strict)
			}
		})
	}
}

// TestStrictSchemaGainsTheClosedObject pins the rewrite: the one thing the API
// demands of every object is additionalProperties:false, and a tool schema
// written for prose leaves it out.
func TestStrictSchemaGainsTheClosedObject(t *testing.T) {
	compat := anthropic.Compat{StrictTools: true}
	payload := decodeRequest(t, captureWith(t, compat, toolRequest(readTool(true))))
	schema := objectField(t, firstTool(t, payload), "input_schema")

	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties: got %#v want false", schema["additionalProperties"])
	}
	if schema["type"] != "object" {
		t.Fatalf("type: got %#v want object", schema["type"])
	}
	// The remaining schema is the caller's, untouched: strict mode validates
	// the arguments, it does not change what the tool asks for.
	properties := objectField(t, schema, "properties")
	if objectField(t, properties, "path")["type"] != "string" {
		t.Fatalf("path property changed: %#v", properties["path"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "path" {
		t.Fatalf("required: got %#v want [path]", schema["required"])
	}
}

// TestStrictSchemaFallsBackForRejectedKeywords pins the guard. The API answers
// 400 for the whole request when a strict schema carries a bound it cannot
// compile, so a schema holding one stays non-strict and reaches the server
// exactly as the caller wrote it.
func TestStrictSchemaFallsBackForRejectedKeywords(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{
			name:   "numeric bound",
			schema: `{"type":"object","properties":{"n":{"type":"integer","minimum":1}}}`,
		},
		{
			name:   "string length",
			schema: `{"type":"object","properties":{"s":{"type":"string","maxLength":10}}}`,
		},
		{
			name:   "array length above one",
			schema: `{"type":"object","properties":{"xs":{"type":"array","items":{"type":"string"},"minItems":2}}}`,
		},
		{
			name:   "format outside the accepted set",
			schema: `{"type":"object","properties":{"s":{"type":"string","format":"regex"}}}`,
		},
		{
			name:   "open object declared by the caller",
			schema: `{"type":"object","properties":{"s":{"type":"string"}},"additionalProperties":true}`,
		},
		{
			name:   "union member",
			schema: `{"type":"object","properties":{"s":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}`,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tool := wire.Tool{
				Name:        "read",
				Description: "Read a file",
				Parameters:  json.RawMessage(test.schema),
				Strict:      true,
			}
			compat := anthropic.Compat{StrictTools: true}
			payload := decodeRequest(t, captureWith(t, compat, toolRequest(tool)))
			def := firstTool(t, payload)

			if strict, present := def["strict"]; present {
				t.Fatalf("strict present for a refused schema: %#v", strict)
			}
			goldenBody(t, normalizeBytes(t, def["input_schema"]), test.schema)
		})
	}
}

// TestStrictSchemaAcceptsTheSupportedSubset pins the other side of the guard: a
// schema using only keywords constrained decoding understands keeps its strict
// flag even when it is nested.
func TestStrictSchemaAcceptsTheSupportedSubset(t *testing.T) {
	const schema = `{
		"type": "object",
		"properties": {
			"mode": {"type": "string", "enum": ["fast", "slow"]},
			"tags": {"type": "array", "items": {"type": "string"}, "minItems": 1},
			"when": {"type": "string", "format": "date"},
			"nested": {"type": "object", "properties": {"n": {"type": "number"}}}
		},
		"required": ["mode"]
	}`
	tool := wire.Tool{
		Name:        "search",
		Description: "Search",
		Parameters:  json.RawMessage(schema),
		Strict:      true,
	}
	payload := decodeRequest(t, captureWith(t, anthropic.Compat{StrictTools: true}, toolRequest(tool)))
	def := firstTool(t, payload)

	if def["strict"] != true {
		t.Fatalf("strict: got %#v want true", def["strict"])
	}
	input := objectField(t, def, "input_schema")
	if input["additionalProperties"] != false {
		t.Fatalf("root additionalProperties: got %#v want false", input["additionalProperties"])
	}
	nested := objectField(t, objectField(t, input, "properties"), "nested")
	if nested["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties: got %#v want false", nested["additionalProperties"])
	}
	// The optional property stays optional: the API budgets optional
	// parameters, it does not forbid them.
	if required := input["required"]; len(required.([]any)) != 1 {
		t.Fatalf("required: got %#v want one entry", required)
	}
}

// TestStrictToolsStopAtTheCeiling pins the documented limit of twenty strict
// tools per request. Beyond it the API rejects the call, so the twenty-first
// tool travels loose instead.
func TestStrictToolsStopAtTheCeiling(t *testing.T) {
	tools := make([]wire.Tool, 0, 21)
	for i := 0; i < 21; i++ {
		tools = append(tools, readTool(true))
		tools[i].Name = fmt.Sprintf("tool%d", i)
	}

	payload := decodeRequest(t, captureWith(t, anthropic.Compat{StrictTools: true}, toolRequest(tools...)))
	declared, ok := payload["tools"].([]any)
	if !ok || len(declared) != 21 {
		t.Fatalf("tools: got %#v want 21 entries", payload["tools"])
	}

	strict := 0
	for _, entry := range declared {
		def, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %#v", entry)
		}
		if _, present := def["strict"]; present {
			strict++
		}
	}
	if strict != 20 {
		t.Fatalf("strict tools: got %d want 20", strict)
	}
}

// firstTool returns the single tool definition of a decoded request.
func firstTool(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	declared, ok := payload["tools"].([]any)
	if !ok || len(declared) == 0 {
		t.Fatalf("tools: got %#v", payload["tools"])
	}
	def, ok := declared[0].(map[string]any)
	if !ok {
		t.Fatalf("tool entry is not an object: %#v", declared[0])
	}
	return def
}

// normalizeBytes encodes a decoded schema back to JSON so a whole-body
// comparison can be used on a fragment.
func normalizeBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return data
}

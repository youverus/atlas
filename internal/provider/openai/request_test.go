package openai_test

// The request-side fields of 0.12.12: the standard reasoning dial, tool choice,
// and store. Each is a statement about one endpoint, so each is driven by a
// compat switch and asserted as a whole body rather than as a key lookup: a
// field that travels in the wrong place is as wrong as one that does not travel.

import (
	"encoding/json"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
)

// reasoningRequest is one user turn for a model that declares reasoning.
func reasoningRequest(level string) provider.Request {
	return provider.Request{
		Model: wire.Model{
			ID:        "gpt-x",
			API:       openai.APIName,
			Provider:  "openai",
			Reasoning: true,
		},
		Messages: []wire.Message{wire.UserMessage("hi")},
		Options:  provider.Options{ThinkingLevel: level},
	}
}

// TestEffortDialTravelsUnderTheStandardDialect pins atlas's gears against the
// effort vocabulary the chat completions API documents. high is the ceiling, so
// the three gears above it clamp; off has no value there and is left out.
func TestEffortDialTravelsUnderTheStandardDialect(t *testing.T) {
	const head = `"model": "gpt-x",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"stream_options": {"include_usage": true}`

	cases := []struct {
		level string
		want  string
	}{
		{level: "", want: `{` + head + `}`},
		// off cannot be said: this API has no "none", and inventing one would
		// be a request the endpoint rejects. The vendor default stands.
		{level: "off", want: `{` + head + `}`},
		{level: "low", want: `{` + head + `, "reasoning_effort": "low"}`},
		{level: "medium", want: `{` + head + `, "reasoning_effort": "medium"}`},
		{level: "high", want: `{` + head + `, "reasoning_effort": "high"}`},
		{level: "xhigh", want: `{` + head + `, "reasoning_effort": "high"}`},
		{level: "max", want: `{` + head + `, "reasoning_effort": "high"}`},
		{level: "ultra", want: `{` + head + `, "reasoning_effort": "high"}`},
		// An unknown gear must not read as "say nothing": the caller asked for
		// thinking, so the vendor default is written explicitly.
		{level: "bogus", want: `{` + head + `, "reasoning_effort": "high"}`},
	}

	for _, test := range cases {
		name := "level=" + test.level
		t.Run(name, func(t *testing.T) {
			goldenBody(t, captureWith(t, openai.Compat{}, reasoningRequest(test.level)), test.want)
		})
	}
}

// TestEffortNeedsTheModelToDeclareReasoning pins the asymmetry with the DeepSeek
// dialect: there the vendor thinks by default, so the level alone decides, while
// an endpoint that merely borrowed the id must not start receiving a field it
// never agreed to read.
func TestEffortNeedsTheModelToDeclareReasoning(t *testing.T) {
	req := reasoningRequest("high")
	req.Model.Reasoning = false
	want := `{
		"model": "gpt-x",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"stream_options": {"include_usage": true}
	}`
	goldenBody(t, captureWith(t, openai.Compat{}, req), want)
}

// TestEffortCanBeTurnedOff is the escape hatch for a gateway that rejects the
// field instead of ignoring it.
func TestEffortCanBeTurnedOff(t *testing.T) {
	want := `{
		"model": "gpt-x",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"stream_options": {"include_usage": true}
	}`
	compat := openai.Compat{DisableReasoningEffort: true}
	goldenBody(t, captureWith(t, compat, reasoningRequest("high")), want)
}

// TestToolChoiceTravels pins that the switch is passed through verbatim and that
// the empty value keeps the field off the wire.
func TestToolChoiceTravels(t *testing.T) {
	tools := []wire.Tool{{
		Name:       "echo",
		Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}}
	for _, choice := range []string{"", "auto", "none", "required"} {
		t.Run("tool_choice="+choice, func(t *testing.T) {
			req := reasoningRequest("")
			req.Messages = wire.NormalizeContext("You are pi.", tools, []wire.Message{wire.UserMessage("hi")})
			body := captureWith(t, openai.Compat{ToolChoice: choice}, req)
			decoded := decodeRequest(t, body)
			got, present := decoded["tool_choice"]
			if choice == "" {
				if present {
					t.Fatalf("tool_choice travelled without being asked for: %v", got)
				}
				return
			}
			if got != choice {
				t.Fatalf("tool_choice: got %v, want %q", got, choice)
			}
		})
	}
}

// TestStoreIsWrittenWhenAsked pins both halves of a tri-state: the field carries
// false when the switch is on, and stays absent when it is off. An endpoint that
// keeps responses by default reads those two differently.
func TestStoreIsWrittenWhenAsked(t *testing.T) {
	t.Run("asked", func(t *testing.T) {
		want := `{
			"model": "gpt-x",
			"messages": [{"role": "user", "content": "hi"}],
			"stream": true,
			"stream_options": {"include_usage": true},
			"store": false
		}`
		goldenBody(t, captureWith(t, openai.Compat{SendStore: true}, reasoningRequest("")), want)
	})
	t.Run("not asked", func(t *testing.T) {
		body := captureWith(t, openai.Compat{}, reasoningRequest(""))
		if _, present := decodeRequest(t, body)["store"]; present {
			t.Fatalf("store travelled without being asked for: %s", body)
		}
	})
}

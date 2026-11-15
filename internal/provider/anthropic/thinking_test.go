package anthropic_test

import (
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/wire"
)

// deepSeekRequest is the smallest request that reaches the thinking fields: one
// user turn, a temperature that must not travel with thinking, and the level
// under test. The model declares no reasoning, on purpose: DeepSeek thinks by
// default, so the level alone has to decide.
func deepSeekRequest(level string) provider.Request {
	return provider.Request{
		Model: wire.Model{
			ID:        "deepseek-chat",
			API:       anthropic.APIName,
			Provider:  "deepseek",
			MaxTokens: 32768,
		},
		Messages: []wire.Message{wire.UserMessage("hi")},
		Options:  provider.Options{Temperature: wire.Ptr(0.4), ThinkingLevel: level},
	}
}

// captureWith sends one request through a provider built from compat and
// returns the body that reached the server.
func captureWith(t *testing.T, compat anthropic.Compat, req provider.Request) []byte {
	t.Helper()
	server := sseServer(t, "text.sse")
	compat.BaseURL = server.URL + "/v1"
	if _, _, err := provider.Collect(mustStream(t, anthropic.New(compat), req)); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return server.last(t).body
}

// goldenBody compares a captured request against a whole-body fixture. A diff
// here means a field moved, so the failure prints both documents.
func goldenBody(t *testing.T, got []byte, want string) {
	t.Helper()
	normalizedGot := normalizeJSON(t, "request", got)
	normalizedWant := normalizeJSON(t, "golden", []byte(want))
	if normalizedGot != normalizedWant {
		t.Fatalf("request body differs\n got:\n%s\nwant:\n%s", normalizedGot, normalizedWant)
	}
}

// TestDeepSeekThinkingGolden pins the four states this dialect can be in as
// whole request bodies: nothing for an unset level, the documented toggle for
// off, and the toggle plus an effort dial for low and max.
func TestDeepSeekThinkingGolden(t *testing.T) {
	const head = `"model": "deepseek-chat",
		"max_tokens": 32768,
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true`

	cases := []struct {
		name  string
		level string
		want  string
	}{
		{
			// An empty level asks for nothing, and DeepSeek already thinks at
			// high effort by itself, so temperature travels as before.
			name: "no level leaves the vendor default alone",
			want: `{` + head + `, "temperature": 0.4}`,
		},
		{
			// Off is the toggle DeepSeek documents for both of its formats.
			// Thinking is off again, so temperature is allowed to travel.
			name:  "off disables the mode",
			level: "off",
			want:  `{` + head + `, "thinking": {"type": "disabled"}, "temperature": 0.4}`,
		},
		{
			// Thinking runs, so temperature is dropped: the effort dial and a
			// sampling temperature do not compose on this endpoint.
			name:  "low",
			level: "low",
			want:  `{` + head + `, "thinking": {"type": "enabled"}, "output_config": {"effort": "low"}}`,
		},
		{
			name:  "max",
			level: "max",
			want:  `{` + head + `, "thinking": {"type": "enabled"}, "output_config": {"effort": "max"}}`,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			compat := anthropic.Compat{ThinkingDialect: "deepseek"}
			goldenBody(t, captureWith(t, compat, deepSeekRequest(test.level)), test.want)
		})
	}
}

// TestThinkingDialectEmptyKeepsTheBudget pins that a config without the dialect
// still sends the token budget the messages API documents, and never the
// DeepSeek dial. A real Anthropic endpoint rejects output_config.
func TestThinkingDialectEmptyKeepsTheBudget(t *testing.T) {
	request := deepSeekRequest("high")
	request.Model.Reasoning = true

	want := `{
		"model": "deepseek-chat",
		"max_tokens": 32768,
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"thinking": {"type": "enabled", "budget_tokens": 16384}
	}`
	goldenBody(t, captureWith(t, anthropic.Compat{}, request), want)
}

// TestDeepSeekEffortMapping pins the whole level table: atlas's six effort gears
// collapse onto the three DeepSeek has, off carries the toggle without an effort,
// and an empty level sends neither field.
func TestDeepSeekEffortMapping(t *testing.T) {
	cases := []struct {
		level  string
		toggle string // empty means no thinking field
		effort string // empty means no output_config field
	}{
		{level: ""},
		{level: "off", toggle: "disabled"},
		{level: "low", toggle: "enabled", effort: "low"},
		{level: "medium", toggle: "enabled", effort: "high"},
		{level: "high", toggle: "enabled", effort: "high"},
		{level: "xhigh", toggle: "enabled", effort: "high"},
		{level: "max", toggle: "enabled", effort: "max"},
		{level: "ultra", toggle: "enabled", effort: "max"},
		// An unknown level must not read as "say nothing": the caller asked
		// for thinking, and high is the vendor default.
		{level: "bogus", toggle: "enabled", effort: "high"},
	}

	for _, test := range cases {
		t.Run("level="+test.level, func(t *testing.T) {
			compat := anthropic.Compat{ThinkingDialect: "deepseek"}
			payload := decodeRequest(t, captureWith(t, compat, deepSeekRequest(test.level)))

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

package openai_test

import (
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
)

// deepSeekRequest is the smallest request that reaches the thinking fields: one
// user turn, a temperature that must not travel with thinking, and the level
// under test. The model declares no reasoning, on purpose: DeepSeek thinks by
// default, so the level alone has to decide.
func deepSeekRequest(level string) provider.Request {
	return provider.Request{
		Model:    wire.Model{ID: "deepseek-chat", API: openai.APIName, Provider: "deepseek"},
		Messages: []wire.Message{wire.UserMessage("hi")},
		Options:  provider.Options{Temperature: wire.Ptr(0.4), ThinkingLevel: level},
	}
}

// captureWith sends one request through a provider built from compat and
// returns the body that reached the server.
func captureWith(t *testing.T, compat openai.Compat, req provider.Request) []byte {
	t.Helper()
	server := sseServer(t, "openai_text.sse")
	compat.BaseURL = server.URL
	collect(t, openai.New(compat), req)
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

// TestDeepSeekThinkingGolden pins the three states this dialect can be in as
// whole request bodies. Each message shape is the same, so a field appearing or
// disappearing is the only difference between them.
func TestDeepSeekThinkingGolden(t *testing.T) {
	const head = `"model": "deepseek-chat",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"stream_options": {"include_usage": true}`

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
			// off has to be said out loud: the vendor default is enabled.
			name:  "off",
			level: "off",
			want:  `{` + head + `, "temperature": 0.4, "thinking": {"type": "disabled"}}`,
		},
		{
			// Thinking runs, so temperature is dropped: DeepSeek ignores it
			// there and sending one would suggest the two compose.
			name:  "low",
			level: "low",
			want:  `{` + head + `, "thinking": {"type": "enabled"}, "reasoning_effort": "low"}`,
		},
		{
			name:  "max",
			level: "max",
			want:  `{` + head + `, "thinking": {"type": "enabled"}, "reasoning_effort": "max"}`,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			compat := openai.Compat{ThinkingDialect: "deepseek"}
			goldenBody(t, captureWith(t, compat, deepSeekRequest(test.level)), test.want)
		})
	}
}

// TestThinkingDialectEmptySendsNoVendorFields pins that the DeepSeek shape never
// leaks into the standard dialect: no thinking object and no DeepSeek effort
// value, whatever the level. The standard dialect has its own effort field,
// which needs the model to declare reasoning — the request below does not, so
// neither field travels (request_test.go covers the other half).
func TestThinkingDialectEmptySendsNoVendorFields(t *testing.T) {
	want := `{
		"model": "deepseek-chat",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"stream_options": {"include_usage": true},
		"temperature": 0.4
	}`
	for _, level := range []string{"", "off", "high", "max"} {
		t.Run("level="+level, func(t *testing.T) {
			goldenBody(t, captureWith(t, openai.Compat{}, deepSeekRequest(level)), want)
		})
	}
}

// TestDeepSeekEffortMapping pins the whole level table: atlas's six effort gears
// collapse onto the three DeepSeek has, and off is the only level that disables
// thinking.
func TestDeepSeekEffortMapping(t *testing.T) {
	cases := []struct {
		level      string
		wantType   string // empty means no thinking object at all
		wantEffort string // empty means no reasoning_effort field
	}{
		{level: ""},
		{level: "off", wantType: "disabled"},
		{level: "low", wantType: "enabled", wantEffort: "low"},
		{level: "medium", wantType: "enabled", wantEffort: "high"},
		{level: "high", wantType: "enabled", wantEffort: "high"},
		{level: "xhigh", wantType: "enabled", wantEffort: "high"},
		{level: "max", wantType: "enabled", wantEffort: "max"},
		{level: "ultra", wantType: "enabled", wantEffort: "max"},
		// An unknown level must not read as "say nothing": the caller asked for
		// thinking, and high is the vendor default.
		{level: "bogus", wantType: "enabled", wantEffort: "high"},
	}

	for _, test := range cases {
		t.Run("level="+test.level, func(t *testing.T) {
			compat := openai.Compat{ThinkingDialect: "deepseek"}
			payload := decodeRequest(t, captureWith(t, compat, deepSeekRequest(test.level)))

			raw, present := payload["thinking"]
			if test.wantType == "" {
				if present {
					t.Fatalf("thinking present without a level: %#v", raw)
				}
			} else {
				thinking, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("thinking: %#v", raw)
				}
				if thinking["type"] != test.wantType {
					t.Fatalf("thinking type: got %#v want %q", thinking["type"], test.wantType)
				}
			}

			effort, present := payload["reasoning_effort"]
			switch test.wantEffort {
			case "":
				if present {
					t.Fatalf("reasoning_effort present: %#v", effort)
				}
			default:
				if effort != test.wantEffort {
					t.Fatalf("reasoning_effort: got %#v want %q", effort, test.wantEffort)
				}
			}
		})
	}
}

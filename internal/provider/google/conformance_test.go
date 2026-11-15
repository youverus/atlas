package google_test

// The conformance suite for the generative language adapter. The cases are this
// protocol's own events, but the promises are shared with the other adapters:
// one terminal event, blocks that add up to their deltas, and a message whose
// stop reason and usage mean what the wire said.
//
// Two of those promises read differently here: this API names the model and the
// streaming choice in the URL rather than the body, which the suite has to be
// told before it can audit a correct request.

import (
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/conformance"
	"pi-go/internal/provider/google"
	"pi-go/internal/wire"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(model wire.Model) provider.Provider {
		return google.New(google.Compat{})
	}, conformance.Suite{
		Model:        wire.Model{ID: "conformance-model", API: google.APIName, Provider: "probe"},
		Path:         "/models/conformance-model:streamGenerateContent",
		HeaderKey:    "x-goog-api-key",
		ModelInPath:  true,
		StreamInPath: true,
		Cases: []conformance.Case{
			{
				Name: "text",
				Body: fixture(t, "text.sse"),
				Want: conformance.Want{
					Text:  "Hello world!",
					Stop:  wire.StopStop,
					Usage: &wire.Usage{Input: 8, Output: 7, CacheRead: 3, TotalTokens: 18},
				},
			},
			{
				// The thought summary arrives as its own part, and its tokens are
				// counted into the answer.
				Name: "thinking then text",
				Body: fixture(t, "thinking.sse"),
				Want: conformance.Want{
					Text:      "Done",
					Thinking:  "Let me think",
					Signature: "c2lnLWE=",
					Stop:      wire.StopStop,
					Usage:     &wire.Usage{Input: 20, Output: 12, Reasoning: wire.Ptr(8), TotalTokens: 32},
				},
			},
			{
				// A call arrives whole, so the block has to be assembled from one
				// chunk rather than a run of deltas.
				Name: "a tool call",
				Body: fixture(t, "tool_call.sse"),
				Want: conformance.Want{
					Text:      "Checking.",
					Stop:      wire.StopToolUse,
					Usage:     &wire.Usage{Input: 30, Output: 20, TotalTokens: 50},
					ToolCalls: []conformance.ToolCall{{ID: "read_1", Name: "read", Arguments: `{"path":"a.go"}`}},
				},
			},
			{
				Name: "a stream error after partial text",
				Body: fixture(t, "error.sse"),
				Want: conformance.Want{
					Text:  "Partial",
					Error: "Internal error",
				},
			},
			{
				Name: "stream truncated before the finish reason",
				Body: fixture(t, "truncated.sse"),
				Want: conformance.Want{
					Text:  "Cut off",
					Error: "ended without a finish reason",
				},
			},
			{
				Name:   "server error",
				Status: 500,
				Body:   `{"error":{"code":500,"message":"Internal error","status":"INTERNAL"}}`,
				Want: conformance.Want{
					Error: "Internal error",
				},
			},
		},
	})
}

// fixture reads a recorded stream from the google fixtures.
func fixture(t *testing.T, name string) string {
	t.Helper()
	return string(readFixture(t, name))
}

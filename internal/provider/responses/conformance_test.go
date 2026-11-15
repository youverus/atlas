package responses_test

// The conformance suite for the Responses adapter. These cases are this
// protocol's own events; the promises are the ones every adapter keeps, so a
// translation that loses a delta, a call, or a stop reason fails here.

import (
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/conformance"
	"pi-go/internal/provider/responses"
	"pi-go/internal/wire"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(model wire.Model) provider.Provider {
		return responses.New(responses.Compat{})
	}, conformance.Suite{
		Model:     wire.Model{ID: "conformance-model", API: responses.APIName, Provider: "probe"},
		Path:      "/responses",
		HeaderKey: "Authorization",
		Cases: []conformance.Case{
			{
				Name: "text",
				Body: fixture(t, "text.sse"),
				Want: conformance.Want{
					Text:  "Hello world",
					Stop:  wire.StopStop,
					Usage: &wire.Usage{Input: 9, Output: 2, TotalTokens: 11},
				},
			},
			{
				// The reasoning item is stored whole, so only the text and the
				// accounting are asserted here; the signature has its own test.
				Name: "reasoning then text",
				Body: fixture(t, "reasoning.sse"),
				Want: conformance.Want{
					Text:     "Let me look.",
					Thinking: "We need to read the file.",
					Stop:     wire.StopStop,
					Usage:    &wire.Usage{Input: 8, Output: 8, CacheRead: 12, Reasoning: wire.Ptr(5), TotalTokens: 28},
				},
			},
			{
				Name: "a streamed tool call",
				Body: fixture(t, "tool_call.sse"),
				Want: conformance.Want{
					Stop:      wire.StopToolUse,
					Usage:     &wire.Usage{Input: 30, Output: 20, TotalTokens: 50},
					ToolCalls: []conformance.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`}},
				},
			},
			{
				// A response cut off at the output limit is a length stop, not a
				// failure: the half message is what the user asked to see.
				Name: "incomplete at the output limit",
				Body: fixture(t, "incomplete.sse"),
				Want: conformance.Want{
					Text:  "Hello",
					Stop:  wire.StopLength,
					Usage: &wire.Usage{Input: 5, Output: 3, TotalTokens: 8},
				},
			},
			{
				Name: "a failed response",
				Body: fixture(t, "failed.sse"),
				Want: conformance.Want{
					Text:  "Half a",
					Error: "server_error: the model crashed",
				},
			},
			{
				// No terminal response event: the last item may be half written,
				// so this must not look complete.
				Name: "stream truncated before the terminal event",
				Body: fixture(t, "truncated.sse"),
				Want: conformance.Want{
					Text:  "Cut off",
					Error: "ended before a terminal response event",
				},
			},
			{
				Name:   "server error",
				Status: 500,
				Body:   `{"error":{"message":"you are over quota"}}`,
				Want: conformance.Want{
					Error: "you are over quota",
				},
			},
		},
	})
}

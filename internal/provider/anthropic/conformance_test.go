package anthropic_test

// The conformance suite for the messages adapter. The cases are this
// protocol's own events, but the promises are shared with the other adapters:
// one terminal event, blocks that add up to their deltas, and a message whose
// stop reason and usage mean what the wire said.

import (
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/conformance"
	"pi-go/internal/wire"
)

// fixture reads a recorded stream from the anthropic fixtures.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "anthropic", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(model wire.Model) provider.Provider {
		return anthropic.New(anthropic.Compat{})
	}, conformance.Suite{
		Model:     wire.Model{ID: "conformance-model", API: anthropic.APIName, Provider: "probe"},
		Path:      "/messages",
		HeaderKey: "x-api-key",
		Cases: []conformance.Case{
			{
				Name: "text",
				Body: fixture(t, "text.sse"),
				Want: conformance.Want{
					Text:  "Hello world",
					Stop:  wire.StopStop,
					Usage: &wire.Usage{Input: 11, Output: 7, CacheRead: 3, CacheWrite: 2, TotalTokens: 23},
				},
			},
			{
				// The signature arrives in fragments after the reasoning text, so
				// assembling it is part of the promise.
				Name: "thinking then text",
				Body: fixture(t, "thinking.sse"),
				Want: conformance.Want{
					Text:      "Done",
					Thinking:  "Let me think",
					Signature: "sig-asig-b",
					Stop:      wire.StopStop,
					Usage:     &wire.Usage{Input: 20, Output: 12, Reasoning: wire.Ptr(8), TotalTokens: 32},
				},
			},
			{
				Name: "redacted reasoning",
				Body: fixture(t, "redacted.sse"),
				Want: conformance.Want{
					Text:      "Hi there",
					Thinking:  "[Reasoning redacted]",
					Signature: "opaque-payload",
					Stop:      wire.StopStop,
					Usage:     &wire.Usage{Input: 5, Output: 4, TotalTokens: 9},
				},
			},
			{
				Name: "a tool call",
				Body: fixture(t, "tool_use.sse"),
				Want: conformance.Want{
					Text:      "Checking.",
					Stop:      wire.StopToolUse,
					Usage:     &wire.Usage{Input: 30, Output: 20, TotalTokens: 50},
					ToolCalls: []conformance.ToolCall{{ID: "toolu_1", Name: "read", Arguments: `{"path":"a.go"}`}},
				},
			},
			{
				Name: "a stream error after partial text",
				Body: fixture(t, "error.sse"),
				Want: conformance.Want{
					Text:  "Partial",
					Error: "Overloaded",
				},
			},
			{
				Name: "stream truncated before the stop reason",
				Body: fixture(t, "truncated.sse"),
				Want: conformance.Want{
					Text:  "Cut off",
					Error: "ended without a stop reason",
				},
			},
			{
				Name:   "server error",
				Status: 500,
				Body:   `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
				Want: conformance.Want{
					Error: "Overloaded",
				},
			},
		},
	})
}

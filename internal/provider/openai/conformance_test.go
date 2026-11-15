package openai_test

// The conformance suite: the same promises the other adapters keep, replayed
// against this one's fixtures. What the cases share is not the bytes but the
// answer, so a translation that loses a delta, a call, or a stop reason fails
// here even though every fixture is specific to chat completions.

import (
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/conformance"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
)

// fixture reads a recorded stream. The gate prints these files, so a deleted
// one shows up as a missing line before it shows up as a passing test.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "sse", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(model wire.Model) provider.Provider {
		return openai.New(openai.Compat{})
	}, conformance.Suite{
		Model:     wire.Model{ID: "conformance-model", API: openai.APIName, Provider: "probe"},
		Path:      "/chat/completions",
		HeaderKey: "Authorization",
		Cases: []conformance.Case{
			{
				Name: "text",
				Body: fixture(t, "openai_text.sse"),
				Want: conformance.Want{
					Text:  "Hello world",
					Stop:  wire.StopStop,
					Usage: &wire.Usage{Input: 9, Output: 2, TotalTokens: 11},
				},
			},
			{
				Name: "reasoning then text",
				Body: fixture(t, "openai_reasoning.sse"),
				Want: conformance.Want{
					Text:      "Let me look.",
					Thinking:  "We need to read the file.",
					Signature: "reasoning_content",
					Stop:      wire.StopStop,
					Usage:     &wire.Usage{Input: 8, Output: 8, CacheRead: 12, Reasoning: wire.Ptr(5), TotalTokens: 28},
				},
			},
			{
				Name: "two interleaved tool calls",
				Body: fixture(t, "openai_tool_calls.sse"),
				Want: conformance.Want{
					Stop: wire.StopToolUse,
					ToolCalls: []conformance.ToolCall{
						{ID: "call_a", Name: "read", Arguments: `{"path":"a.txt"}`},
						{ID: "call_b", Name: "grep", Arguments: `{"pattern":"x"}`},
					},
				},
			},
			{
				// The vendor closes the stream without ever saying why. Half a
				// message must not look like a whole one.
				Name: "stream truncated before the finish reason",
				Body: fixture(t, "openai_truncated.sse"),
				Want: conformance.Want{
					Text:  "half a sen",
					Error: "stream ended without finish_reason",
				},
			},
			{
				// A server that rejects the request still answers with a stream:
				// an adapter reports it as an assistant message, so every caller
				// reads one path.
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

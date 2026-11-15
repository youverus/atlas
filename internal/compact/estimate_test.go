package compact_test

import (
	"strings"
	"testing"

	"pi-go/internal/compact"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

func TestEstimateTokensPerRole(t *testing.T) {
	cases := []struct {
		name    string
		message wire.Message
		want    int
	}{
		{
			name:    "user text rounds up",
			message: wire.UserMessage("hello world"),
			want:    3, // 11 characters, ceil(11/4)
		},
		{
			name:    "assistant text",
			message: wire.AssistantMessage(wire.TextContent("abcdefgh")),
			want:    2,
		},
		{
			name:    "assistant counts thinking",
			message: wire.AssistantMessage(wire.ThinkingContent("abcdefgh", "sig")),
			want:    2,
		},
		{
			name:    "assistant counts tool call name and arguments",
			message: wire.AssistantMessage(wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go"}`))),
			want:    5, // 4 + 15 characters -> ceil(19/4)
		},
		{
			name:    "tool result text",
			message: wire.ToolResultMessage("call_1", "read", false, wire.TextContent("package a")),
			want:    3,
		},
		{
			name:    "image costs a flat rate",
			message: wire.UserMessageBlocks(wire.ImageContent("aGk=", "image/png")),
			want:    1200, // 4800 characters / 4
		},
		{
			name:    "text and image add up",
			message: wire.UserMessageBlocks(wire.TextContent("look"), wire.ImageContent("aGk=", "image/png")),
			want:    1201,
		},
		{
			name:    "empty message is free",
			message: wire.UserMessage(""),
			want:    0,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := compact.EstimateTokens(test.message); got != test.want {
				t.Fatalf("tokens: got %d want %d", got, test.want)
			}
		})
	}
}

// TestEstimateTokensCountsCJKByBytes pins the deliberate deviation from pi: a
// CJK glyph is about one token and three UTF-8 bytes, while dividing the
// character count by four would report a quarter of a token.
func TestEstimateTokensCountsCJKByBytes(t *testing.T) {
	text := strings.Repeat("读", 8) // 8 glyphs, 24 bytes
	got := compact.EstimateTokens(wire.UserMessage(text))
	if got != 6 { // ceil(24/4)
		t.Fatalf("tokens: got %d want 6", got)
	}
	if ascii := compact.EstimateTokens(wire.UserMessage(strings.Repeat("x", 24))); ascii != 6 {
		t.Fatalf("ascii tokens: got %d want 6", ascii)
	}
}

func TestEstimateTokensForSystemMessage(t *testing.T) {
	message := wire.SystemMessage("prompt")
	section := "## Memory\nstuff"
	message.Sections = map[string]*string{"memory": &section}
	message.ToolsAdded = []wire.Tool{{Name: "read", Description: "Read", Parameters: []byte(`{"type":"object"}`)}}

	got := compact.EstimateTokens(message)
	// prompt(6) + section(16) + tool JSON(~70), all within a few tokens.
	if got < 20 || got > 40 {
		t.Fatalf("tokens: got %d, want the prompt, sections, and tool declarations counted", got)
	}
}

func TestContextTokensFallsBackToComponents(t *testing.T) {
	if got := compact.ContextTokens(wire.Usage{TotalTokens: 42, Input: 1}); got != 42 {
		t.Fatalf("total: got %d want 42", got)
	}
	usage := wire.Usage{Input: 10, Output: 5, CacheRead: 3, CacheWrite: 2}
	if got := compact.ContextTokens(usage); got != 20 {
		t.Fatalf("components: got %d want 20", got)
	}
}

func TestEstimateContextTokensWithoutUsage(t *testing.T) {
	usage := compact.EstimateContextTokens([]wire.Message{
		wire.UserMessage(filler(10)),
		wire.AssistantMessage(wire.TextContent(filler(20))),
	})
	if usage.LastUsageIndex != -1 {
		t.Fatalf("last usage index: got %d want -1", usage.LastUsageIndex)
	}
	if usage.Tokens != usage.TrailingTokens || usage.UsageTokens != 0 {
		t.Fatalf("estimate: %+v", usage)
	}
	if usage.Tokens < 30 {
		t.Fatalf("tokens: got %d, want at least the 30 estimated", usage.Tokens)
	}
}

func TestEstimateContextTokensAnchorsOnUsage(t *testing.T) {
	message := wire.AssistantMessage(wire.TextContent("done"))
	message.Usage = &wire.Usage{Input: 1000, Output: 200, TotalTokens: 1200}
	usage := compact.EstimateContextTokens([]wire.Message{
		wire.UserMessage(filler(10)),
		message,
		wire.UserMessage(filler(40)),
	})
	if usage.LastUsageIndex != 1 {
		t.Fatalf("last usage index: got %d want 1", usage.LastUsageIndex)
	}
	if usage.UsageTokens != 1200 {
		t.Fatalf("usage tokens: got %d want 1200", usage.UsageTokens)
	}
	if usage.Tokens != 1200+usage.TrailingTokens {
		t.Fatalf("tokens: got %d", usage.Tokens)
	}
	if usage.TrailingTokens < 40 {
		t.Fatalf("trailing: got %d, want the messages after the usage", usage.TrailingTokens)
	}
}

// TestEstimateContextTokensSkipsUnusableUsage pins that a failed or aborted turn
// does not anchor the estimate: its usage describes a context that produced no
// answer.
func TestEstimateContextTokensSkipsUnusableUsage(t *testing.T) {
	failed := wire.AssistantMessage(wire.TextContent(""))
	failed.StopReason = wire.StopError
	failed.Usage = &wire.Usage{TotalTokens: 900}

	aborted := wire.AssistantMessage(wire.TextContent(""))
	aborted.StopReason = wire.StopAborted
	aborted.Usage = &wire.Usage{TotalTokens: 800}

	zero := wire.AssistantMessage(wire.TextContent(""))
	zero.Usage = &wire.Usage{}

	usage := compact.EstimateContextTokens([]wire.Message{failed, aborted, zero})
	if usage.LastUsageIndex != -1 {
		t.Fatalf("last usage index: got %d want -1", usage.LastUsageIndex)
	}
}

func TestEstimateProjectionTrustsFreshUsage(t *testing.T) {
	build := newChain().user(filler(10)).message(withUsage(wire.TextContent("done"), 500))
	path := build.entries()
	projection, err := session.BuildProjection(path, leaf(build))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	usage := compact.EstimateProjection(projection, path)
	if usage.UsageTokens != 500 {
		t.Fatalf("usage tokens: got %d want 500 (usage should be trusted)", usage.UsageTokens)
	}
}

// TestEstimateProjectionDistrustsStaleUsage pins the rule that a usage record
// captured before a compaction describes a context that no longer exists. The
// assistant message survives the cut, so it is still in the projection; only its
// position on the branch reveals that the usage predates the compaction.
func TestEstimateProjectionDistrustsStaleUsage(t *testing.T) {
	build := newChain().user(filler(10)).message(withUsage(wire.TextContent("done"), 5000))
	kept := entryName(1)
	build.compaction("summary", kept).user(filler(10))

	path := build.entries()
	projection, err := session.BuildProjection(path, leaf(build))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if len(projection.Messages) != 3 {
		t.Fatalf("messages: got %d, want compaction, kept assistant, and user", len(projection.Messages))
	}
	usage := compact.EstimateProjection(projection, path)
	if usage.UsageTokens != 0 {
		t.Fatalf("usage tokens: got %d want 0 (usage predates the compaction)", usage.UsageTokens)
	}
	if usage.Tokens < 10 {
		t.Fatalf("tokens: got %d, want the whole projection re-estimated", usage.Tokens)
	}
}

func TestShouldCompact(t *testing.T) {
	settings := compact.DefaultSettings
	cases := []struct {
		name    string
		tokens  int
		window  int
		enabled bool
		want    bool
	}{
		{name: "under the reserve", tokens: 10000, window: 200000, enabled: true, want: false},
		{name: "over the reserve", tokens: 190000, window: 200000, enabled: true, want: true},
		{name: "exactly at the boundary", tokens: 200000 - 16384, window: 200000, enabled: true, want: false},
		{name: "disabled", tokens: 190000, window: 200000, enabled: false, want: false},
		{name: "unknown window never triggers", tokens: 190000, window: 0, enabled: true, want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			settings.Enabled = test.enabled
			if got := compact.ShouldCompact(test.tokens, test.window, settings); got != test.want {
				t.Fatalf("should compact: got %v want %v", got, test.want)
			}
		})
	}
}

// withUsage attaches a usage record to an assistant message.
func withUsage(block wire.Content, total int) wire.Message {
	message := wire.AssistantMessage(block)
	message.StopReason = wire.StopStop
	message.Usage = &wire.Usage{Input: total - 100, Output: 100, TotalTokens: total}
	return message
}

func leaf(build *chain) *string {
	if len(build.entries()) == 0 {
		return nil
	}
	last := build.entries()[len(build.entries())-1].ID
	return &last
}

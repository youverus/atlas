package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

func testUsage(input, output, cacheRead, cacheWrite int, cost float64) wire.Usage {
	return wire.Usage{
		Input:       input,
		Output:      output,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: input + output + cacheRead + cacheWrite,
		Cost: wire.Cost{
			Total: cost,
		},
	}
}

func TestSessionStatsAggregatesMessagesUsageAndContextUsage(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.Model.ContextWindow = 200000
	})

	// Empty session with a declared window reports zero tokens and 0%.
	empty, err := application.SessionStats()
	if err != nil {
		t.Fatalf("empty stats: %v", err)
	}
	if empty.SessionID != manager.ID() || empty.SessionFile != manager.File() {
		t.Fatalf("identity = %+v, want %q / %q", empty, manager.ID(), manager.File())
	}
	if empty.TotalMessages != 0 || empty.Tokens.Total != 0 || empty.Cost != 0 {
		t.Fatalf("empty stats = %+v, want zeroes", empty)
	}
	if empty.ContextUsage == nil || empty.ContextUsage.Tokens == nil || *empty.ContextUsage.Tokens != 0 ||
		empty.ContextUsage.ContextWindow != 200000 || empty.ContextUsage.Percent == nil || *empty.ContextUsage.Percent != 0 {
		t.Fatalf("empty contextUsage = %+v", empty.ContextUsage)
	}

	// Build a branch with user, assistant (with tool call), tool result (with usage),
	// custom message, and a cache-warm usage entry.
	mustAppend(t, manager, wire.UserMessage("hello"))
	assistant := wire.AssistantMessage(
		wire.TextContent("checking"),
		wire.ToolCallContent("c1", "read", json.RawMessage(`{"path":"a.go"}`)),
	)
	u1 := testUsage(100, 50, 20, 10, 0.25)
	assistant.Usage = &u1
	assistant.StopReason = wire.StopToolUse
	mustAppend(t, manager, assistant)

	toolResult := wire.ToolResultMessage("c1", "read", false, wire.TextContent("file body"))
	u2 := testUsage(10, 5, 0, 0, 0.05)
	toolResult.Usage = &u2
	mustAppend(t, manager, toolResult)

	if _, err := manager.AppendCustomMessage("note", json.RawMessage(`"custom"`), true, nil); err != nil {
		t.Fatalf("custom message: %v", err)
	}

	rawWarm, err := json.Marshal(testUsage(2, 1, 97, 0, 0.01))
	if err != nil {
		t.Fatalf("marshal warm usage: %v", err)
	}
	if _, err := manager.AppendUsage("cache_warm", "faux", "faux-1", rawWarm, "warm"); err != nil {
		t.Fatalf("append usage: %v", err)
	}

	stats, err := application.SessionStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.UserMessages != 1 || stats.AssistantMessages != 1 || stats.ToolCalls != 1 ||
		stats.ToolResults != 1 || stats.TotalMessages != 3 {
		t.Fatalf("message counts = %+v, want user=1 assistant=1 calls=1 results=1 total=3", stats)
	}
	wantTokens := TokenStats{Input: 112, Output: 56, CacheRead: 117, CacheWrite: 10, Total: 295}
	if stats.Tokens != wantTokens {
		t.Fatalf("tokens = %+v, want %+v", stats.Tokens, wantTokens)
	}
	if stats.Cost < 0.3099 || stats.Cost > 0.3101 {
		t.Fatalf("cost = %v, want 0.31", stats.Cost)
	}
	if stats.ContextUsage == nil || stats.ContextUsage.Tokens == nil {
		t.Fatalf("contextUsage = %+v, want numeric tokens", stats.ContextUsage)
	}
	// Context estimate anchors on assistant (180 tokens) plus trailing toolResult + custom message.
	if *stats.ContextUsage.Tokens <= 180 {
		t.Fatalf("context tokens = %d, want > 180 (assistant usage + trailing)", *stats.ContextUsage.Tokens)
	}
	wantPercent := (float64(*stats.ContextUsage.Tokens) / 200000.0) * 100.0
	if *stats.ContextUsage.Percent != wantPercent {
		t.Fatalf("percent = %v, want %v", *stats.ContextUsage.Percent, wantPercent)
	}
}

func TestSessionStatsOmitsContextUsageWithoutWindow(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{
		Text:  "hi",
		Usage: testUsage(10, 5, 0, 0, 0.02),
	}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	stats, err := application.SessionStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.ContextUsage != nil {
		t.Fatalf("contextUsage = %+v, want nil when ContextWindow is 0", stats.ContextUsage)
	}
	if stats.Tokens.Total != 15 {
		t.Fatalf("tokens = %+v, want total=15", stats.Tokens)
	}
}

// Immediately after compaction — before a fresh post-compaction assistant
// response reports real usage — tokens and percent stay nil so callers never
// treat stale pre-compaction usage or unanchored estimates as exact usage.
// Session-wide totals still include both pre-compaction and compaction usage.
func TestSessionStatsContextUsageAfterCompaction(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.Model.ContextWindow = 200000
	})

	mustAppend(t, manager, wire.UserMessage("first"))
	a1 := wire.AssistantMessage(wire.TextContent("response1"))
	u1 := testUsage(180000, 0, 0, 0, 1.0)
	a1.Usage = &u1
	a1.StopReason = wire.StopStop
	mustAppend(t, manager, a1)

	kept := mustAppend(t, manager, wire.UserMessage("second"))
	a2 := wire.AssistantMessage(wire.TextContent("response2"))
	u2 := testUsage(195000, 0, 0, 0, 2.0)
	a2.Usage = &u2
	a2.StopReason = wire.StopStop
	mustAppend(t, manager, a2)

	compUsage, err := json.Marshal(testUsage(500, 100, 0, 0, 0.5))
	if err != nil {
		t.Fatalf("marshal compaction usage: %v", err)
	}
	if _, err := manager.AppendCompaction("summary", kept, 195000, session.CompactionOptions{Usage: compUsage}); err != nil {
		t.Fatalf("append compaction: %v", err)
	}
	mustAppend(t, manager, wire.UserMessage("third"))

	stats, err := application.SessionStats()
	if err != nil {
		t.Fatalf("stats after compaction: %v", err)
	}
	if stats.Tokens.Input != 375500 || stats.Tokens.Output != 100 || stats.Tokens.Total != 375600 {
		t.Fatalf("tokens = %+v, want pre-compaction + compaction totals", stats.Tokens)
	}
	if stats.Cost < 3.4999 || stats.Cost > 3.5001 {
		t.Fatalf("cost = %v, want 3.5", stats.Cost)
	}
	if stats.ContextUsage == nil {
		t.Fatal("contextUsage = nil, want struct with null tokens/percent")
	}
	if stats.ContextUsage.ContextWindow != 200000 {
		t.Fatalf("contextWindow = %d, want 200000", stats.ContextUsage.ContextWindow)
	}
	if stats.ContextUsage.Tokens != nil || stats.ContextUsage.Percent != nil {
		t.Fatalf("contextUsage = %+v, want nil tokens and percent immediately after compaction", stats.ContextUsage)
	}

	// Once an assistant message after the compaction carries valid usage,
	// contextUsage resolves to that anchor plus any trailing zero-usage turns.
	a3 := wire.AssistantMessage(wire.TextContent("response3"))
	u3 := testUsage(25000, 0, 0, 0, 0.25)
	a3.Usage = &u3
	a3.StopReason = wire.StopStop
	mustAppend(t, manager, a3)
	mustAppend(t, manager, wire.UserMessage("continue"))
	partial := wire.AssistantMessage(wire.TextContent(strings.Repeat("z", 40)))
	partial.StopReason = wire.StopStop
	mustAppend(t, manager, partial)

	stats, err = application.SessionStats()
	if err != nil {
		t.Fatalf("stats post-compaction response: %v", err)
	}
	if stats.ContextUsage == nil || stats.ContextUsage.Tokens == nil || stats.ContextUsage.Percent == nil {
		t.Fatalf("contextUsage = %+v, want populated post-compaction usage", stats.ContextUsage)
	}
	if *stats.ContextUsage.Tokens <= 25000 {
		t.Fatalf("context tokens = %d, want > 25000", *stats.ContextUsage.Tokens)
	}
	wantPercent := (float64(*stats.ContextUsage.Tokens) / 200000.0) * 100.0
	if *stats.ContextUsage.Percent != wantPercent {
		t.Fatalf("percent = %v, want %v", *stats.ContextUsage.Percent, wantPercent)
	}
}

func TestSessionStatsIncludesBranchSummaryAndAbandonedBranchesInTotals(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New())

	first := mustAppend(t, manager, wire.UserMessage("one"))
	a1 := wire.AssistantMessage(wire.TextContent("abandoned"))
	u1 := testUsage(100, 50, 0, 0, 0.4)
	a1.Usage = &u1
	a1.StopReason = wire.StopStop
	mustAppend(t, manager, a1)

	bsUsage, err := json.Marshal(testUsage(10, 20, 30, 40, 1.0))
	if err != nil {
		t.Fatalf("marshal branch summary usage: %v", err)
	}
	if _, err := manager.BranchWithSummary(&first, "branch summary", nil, nil, bsUsage); err != nil {
		t.Fatalf("branch with summary: %v", err)
	}

	stats, err := application.SessionStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	wantTokens := TokenStats{Input: 110, Output: 70, CacheRead: 30, CacheWrite: 40, Total: 250}
	if stats.Tokens != wantTokens {
		t.Fatalf("tokens = %+v, want %+v", stats.Tokens, wantTokens)
	}
	if stats.Cost < 1.3999 || stats.Cost > 1.4001 {
		t.Fatalf("cost = %v, want 1.4", stats.Cost)
	}
}

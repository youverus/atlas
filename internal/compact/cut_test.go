package compact_test

import (
	"testing"

	"pi-go/internal/compact"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

func TestFindCutPointKeepsTheNewestRange(t *testing.T) {
	build := newChain().
		user(filler(50)).
		assistant(wire.TextContent(filler(50))).
		user(filler(50)).
		assistant(wire.TextContent(filler(50)))
	entries := projected(t, build)

	cut := compact.FindCutPoint(entries, 0, len(entries), 100)
	if cut.FirstKeptEntryIndex != 2 {
		t.Fatalf("first kept: got %d want 2", cut.FirstKeptEntryIndex)
	}
	if cut.IsSplitTurn || cut.TurnStartIndex != -1 {
		t.Fatalf("cut: %+v, want a clean turn start", cut)
	}
}

// TestFindCutPointNeverStartsAtAToolResult pins the rule that matters for the
// next request: a tool result without its tool call is rejected by both wire
// APIs, so no budget may produce that boundary.
func TestFindCutPointNeverStartsAtAToolResult(t *testing.T) {
	build := newChain().
		user("question").
		assistant(wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go"}`))).
		toolResult("call_1", "read", wire.TextContent(filler(200))).
		assistant(wire.TextContent(filler(10))).
		user("next").
		assistant(wire.ToolCallContent("call_2", "read", []byte(`{"path":"b.go"}`))).
		toolResult("call_2", "read", wire.TextContent(filler(200)))
	entries := projected(t, build)

	for budget := 1; budget <= 500; budget += 7 {
		cut := compact.FindCutPoint(entries, 0, len(entries), budget)
		if cut.FirstKeptEntryIndex < 0 || cut.FirstKeptEntryIndex >= len(entries) {
			t.Fatalf("budget %d: index %d out of range", budget, cut.FirstKeptEntryIndex)
		}
		for _, message := range entries[cut.FirstKeptEntryIndex].Messages {
			if message.Role == wire.RoleToolResult {
				t.Fatalf("budget %d: kept range starts at the tool result in entry %d", budget, cut.FirstKeptEntryIndex)
			}
		}
	}
}

func TestFindCutPointReportsSplitTurn(t *testing.T) {
	build := newChain().
		user(filler(10)).
		assistant(wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go"}`))).
		toolResult("call_1", "read", wire.TextContent(filler(100))).
		assistant(wire.TextContent(filler(10)))
	entries := projected(t, build)

	cut := compact.FindCutPoint(entries, 0, len(entries), 50)
	if cut.FirstKeptEntryIndex != 3 {
		t.Fatalf("first kept: got %d want 3 (the assistant after the tool result)", cut.FirstKeptEntryIndex)
	}
	if !cut.IsSplitTurn {
		t.Fatalf("cut: %+v, want a split turn", cut)
	}
	if cut.TurnStartIndex != 0 {
		t.Fatalf("turn start: got %d want 0", cut.TurnStartIndex)
	}
}

// TestFindCutPointPullsMetadataIntoTheKeptRange keeps a thinking-level change
// from being left behind alone: it carries no context, so the walk moves the
// boundary in front of it.
func TestFindCutPointPullsMetadataIntoTheKeptRange(t *testing.T) {
	build := newChain().
		user(filler(100)).
		assistant(wire.TextContent(filler(100))).
		thinkingLevel("high").
		user(filler(100))
	entries := projected(t, build)

	cut := compact.FindCutPoint(entries, 0, len(entries), 100)
	if cut.FirstKeptEntryIndex != 2 {
		t.Fatalf("first kept: got %d want 2 (the thinking-level change)", cut.FirstKeptEntryIndex)
	}
	if entries[2].Source.Type != session.TypeThinkingLevelChange {
		t.Fatalf("entry 2: got %q want a thinking-level change", entries[2].Source.Type)
	}
}

// TestFindCutPointStopsAtAnOmittedEntry pins that the walk does not step past a
// message a context edit removed: it must stay summarized instead of being
// treated as the start of a turn that was already accounted for.
func TestFindCutPointStopsAtAnOmittedEntry(t *testing.T) {
	build := newChain().
		user(filler(100)).
		assistant(wire.TextContent(filler(100))).
		contextEdit("entry-a").
		user(filler(100))
	entries := projected(t, build)
	if len(entries[0].Messages) != 0 {
		t.Fatalf("entry 0: got %d messages, want the context edit to omit it", len(entries[0].Messages))
	}

	cut := compact.FindCutPoint(entries, 0, len(entries), 200)
	if cut.FirstKeptEntryIndex != 1 {
		t.Fatalf("first kept: got %d want 1 (the walk must stop at the omitted entry)", cut.FirstKeptEntryIndex)
	}
}

func TestFindCutPointDegenerateRanges(t *testing.T) {
	build := newChain().user(filler(10)).assistant(wire.TextContent(filler(10)))
	entries := projected(t, build)

	if cut := compact.FindCutPoint(entries, 2, 2, 10); cut.FirstKeptEntryIndex != 2 || cut.TurnStartIndex != -1 {
		t.Fatalf("empty range: %+v", cut)
	}
	// Out-of-range bounds are clamped rather than panicking.
	if cut := compact.FindCutPoint(entries, -5, 99, 10); cut.FirstKeptEntryIndex < 0 || cut.FirstKeptEntryIndex >= len(entries) {
		t.Fatalf("clamped range: %+v", cut)
	}

	// A range with no user or assistant message has no boundary to offer.
	orphan := projected(t, newChain().toolResult("call_1", "read", wire.TextContent(filler(10))))
	cut := compact.FindCutPoint(orphan, 0, len(orphan), 4)
	if cut.FirstKeptEntryIndex != 0 || cut.IsSplitTurn || cut.TurnStartIndex != -1 {
		t.Fatalf("no cut points: %+v", cut)
	}
}

// projected resolves a chain into the projection entries FindCutPoint consumes.
func projected(t *testing.T, build *chain) []session.ProjectedEntry {
	t.Helper()
	projection, err := session.BuildProjection(build.entries(), leaf(build))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	return projection.Entries
}

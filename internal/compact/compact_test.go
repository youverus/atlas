package compact_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/compact"
	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

func TestPrepareReturnsNilWhenThereIsNothingToCompact(t *testing.T) {
	if preparation, err := compact.Prepare(nil, compact.DefaultSettings); err != nil || preparation != nil {
		t.Fatalf("empty path: %+v %v", preparation, err)
	}

	// The tip is already a compaction: compacting it would summarize the summary.
	tip := newChain().user("hello").compaction("summary", "entry-a")
	if preparation, err := compact.Prepare(tip.entries(), compact.DefaultSettings); err != nil || preparation != nil {
		t.Fatalf("compaction tip: %+v %v", preparation, err)
	}

	// Everything fits in the kept range, so there is no range to summarize.
	fits := newChain().user("hello").assistant(wire.TextContent("hi"))
	if preparation, err := compact.Prepare(fits.entries(), compact.DefaultSettings); err != nil || preparation != nil {
		t.Fatalf("small context: %+v %v", preparation, err)
	}
}

func TestPrepareSplitsATurnAndCollectsFiles(t *testing.T) {
	build := newChain().
		user("read the file").
		assistant(wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go"}`))).
		toolResult("call_1", "read", wire.TextContent("package a")).
		user(filler(100)).
		assistant(wire.TextContent(filler(100)))
	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 50})

	if !preparation.IsSplitTurn {
		t.Fatalf("preparation: want a split turn")
	}
	if preparation.FirstKeptEntryID != "entry-e" {
		t.Fatalf("first kept: got %q want entry-e", preparation.FirstKeptEntryID)
	}
	if len(preparation.MessagesToSummarize) != 3 {
		t.Fatalf("history: got %d messages want the user turn, tool call, and result", len(preparation.MessagesToSummarize))
	}
	if len(preparation.TurnPrefixMessages) != 1 {
		t.Fatalf("turn prefix: got %d messages want the user turn the cut splits", len(preparation.TurnPrefixMessages))
	}
	if strings.Join(preparation.ReadFiles, ",") != "a.go" {
		t.Fatalf("read files: %v", preparation.ReadFiles)
	}
	if len(preparation.ModifiedFiles) != 0 {
		t.Fatalf("modified files: %v", preparation.ModifiedFiles)
	}
	if preparation.TokensBefore <= 0 {
		t.Fatalf("tokens before: got %d", preparation.TokensBefore)
	}
	if preparation.PreviousSummary != "" {
		t.Fatalf("previous summary: got %q want none", preparation.PreviousSummary)
	}
}

func TestPrepareKeepsATurnAlignedCutWhole(t *testing.T) {
	build := newChain().
		user(filler(100)).
		assistant(wire.TextContent(filler(100))).
		user(filler(100)).
		assistant(wire.TextContent(filler(100)))
	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 150})

	if preparation.IsSplitTurn {
		t.Fatalf("preparation: a cut on a user message must not split a turn")
	}
	if preparation.FirstKeptEntryID != "entry-c" {
		t.Fatalf("first kept: got %q want entry-c", preparation.FirstKeptEntryID)
	}
	if len(preparation.MessagesToSummarize) != 2 {
		t.Fatalf("history: got %d messages want the first turn", len(preparation.MessagesToSummarize))
	}
	if len(preparation.TurnPrefixMessages) != 0 {
		t.Fatalf("turn prefix: got %d messages want none", len(preparation.TurnPrefixMessages))
	}
}

// TestPrepareUpdatesAnEarlierCompaction pins incremental summarization: a
// branch that already carries a checkpoint switches to update mode and carries
// its file lists forward, so a second compaction does not drop the first one.
func TestPrepareUpdatesAnEarlierCompaction(t *testing.T) {
	build := newChain().user("first question").assistant(wire.TextContent("first answer"))
	build.compaction("previous summary", "entry-a")
	build.list[2].Details = json.RawMessage(`{"readFiles":["old.go"],"modifiedFiles":["legacy.go"]}`)
	build.user(filler(100)).assistant(wire.TextContent(filler(100)))

	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 100})
	if preparation.PreviousSummary != "previous summary" {
		t.Fatalf("previous summary: got %q", preparation.PreviousSummary)
	}
	if strings.Join(preparation.ReadFiles, ",") != "old.go" {
		t.Fatalf("read files: got %v want the earlier compaction's list", preparation.ReadFiles)
	}
	if strings.Join(preparation.ModifiedFiles, ",") != "legacy.go" {
		t.Fatalf("modified files: got %v want the earlier compaction's list", preparation.ModifiedFiles)
	}
	if preparation.FirstKeptEntryID != "entry-e" {
		t.Fatalf("first kept: got %q want entry-e", preparation.FirstKeptEntryID)
	}
}

// TestPrepareIgnoresHookCompactionFiles pins that a checkpoint written by a hook
// carries no file list to replay: its details belong to the hook.
func TestPrepareIgnoresHookCompactionFiles(t *testing.T) {
	build := newChain().user("first question").assistant(wire.TextContent("first answer"))
	build.compaction("hook summary", "entry-a")
	fromHook := true
	build.list[2].FromHook = &fromHook
	build.list[2].Details = json.RawMessage(`{"readFiles":["old.go"],"modifiedFiles":["legacy.go"]}`)
	build.user(filler(100)).assistant(wire.TextContent(filler(100)))

	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 100})
	if len(preparation.ReadFiles) != 0 || len(preparation.ModifiedFiles) != 0 {
		t.Fatalf("files: got %v %v want none", preparation.ReadFiles, preparation.ModifiedFiles)
	}
}

func TestRunJoinsTheSplitTurnSummaries(t *testing.T) {
	build := newChain().
		user("read the file").
		assistant(wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go"}`))).
		toolResult("call_1", "read", wire.TextContent("package a")).
		user(filler(100)).
		assistant(wire.TextContent(filler(100)))
	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 50})

	script := faux.New(
		faux.Response{Text: "HISTORY", Usage: wire.Usage{TotalTokens: 50}},
		faux.Response{Text: "PREFIX", Usage: wire.Usage{TotalTokens: 30}},
	)
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 1000}}

	result, err := summarizer.Run(context.Background(), preparation, compact.DefaultSettings, "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "HISTORY\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX\n\n<read-files>\na.go\n</read-files>"
	if result.Summary != want {
		t.Fatalf("summary: got %q want %q", result.Summary, want)
	}
	if result.Usage.TotalTokens != 80 {
		t.Fatalf("usage: got %+v want both calls combined", result.Usage)
	}
	if result.FirstKeptEntryID != preparation.FirstKeptEntryID || result.TokensBefore != preparation.TokensBefore {
		t.Fatalf("result: got %+v want the preparation's boundary", result)
	}
	details, err := result.Details()
	if err != nil {
		t.Fatalf("details: %v", err)
	}
	if string(details) != `{"readFiles":["a.go"],"modifiedFiles":[]}` {
		t.Fatalf("details: %s", details)
	}

	calls := script.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls: got %d want the history and the turn prefix", len(calls))
	}
	if !strings.Contains(calls[0].Messages[1].Content.Text(), "<conversation>") {
		t.Fatalf("first call: want the history prompt")
	}
	if !strings.Contains(calls[1].Messages[1].Content.Text(), "# Conversation") {
		t.Fatalf("second call: want the turn prefix prompt")
	}
}

// TestRunWithoutHistoryUsesThePreviousSummaryAsTheHistoryHalf covers a cut that
// lands inside the turn right after a checkpoint. pi keeps a placeholder in the
// history half rather than emitting the prefix alone, and the placeholder text
// reaches the session, so it is pinned here.
func TestRunWithoutHistoryUsesThePreviousSummaryAsTheHistoryHalf(t *testing.T) {
	build := newChain().
		compaction("previous summary", "entry-a").
		user(filler(100)).
		assistant(wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go"}`))).
		toolResult("call_1", "read", wire.TextContent(filler(100)))
	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 50})
	if len(preparation.MessagesToSummarize) != 0 || len(preparation.TurnPrefixMessages) == 0 {
		t.Fatalf("preparation: got %d history and %d prefix messages, want a prefix-only split",
			len(preparation.MessagesToSummarize), len(preparation.TurnPrefixMessages))
	}

	script := faux.New(faux.Response{Text: "PREFIX"})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 1000}}
	result, err := summarizer.Run(context.Background(), preparation, compact.DefaultSettings, "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "previous summary\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX"
	if result.Summary != want {
		t.Fatalf("summary: got %q want %q", result.Summary, want)
	}
	if len(script.Calls()) != 1 {
		t.Fatalf("calls: got %d want only the prefix call", len(script.Calls()))
	}
}

func TestRunFailsWhenTheSummaryFails(t *testing.T) {
	build := newChain().
		user(filler(100)).
		assistant(wire.TextContent(filler(100))).
		user(filler(100)).
		assistant(wire.TextContent(filler(100)))
	preparation := prepare(t, build, compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 150})

	script := faux.New(faux.Response{Text: "partial", StopReason: wire.StopLength})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 1000}}
	result, err := summarizer.Run(context.Background(), preparation, compact.DefaultSettings, "")
	if err == nil {
		t.Fatalf("run: got %+v, want a failure", result)
	}
	if result.Summary != "" || result.FirstKeptEntryID != "" || result.TokensBefore != 0 ||
		len(result.ReadFiles) != 0 || len(result.ModifiedFiles) != 0 || result.Usage != (wire.Usage{}) {
		t.Fatalf("result: got %+v want nothing to persist", result)
	}
}

// TestDetailsEncodesEmptyLists keeps the session entry shape stable: pi stores
// arrays, so a compaction without file operations must not write null.
func TestDetailsEncodesEmptyLists(t *testing.T) {
	details, err := compact.Result{}.Details()
	if err != nil {
		t.Fatalf("details: %v", err)
	}
	if string(details) != `{"readFiles":[],"modifiedFiles":[]}` {
		t.Fatalf("details: %s", details)
	}
}

// prepare runs Prepare and fails the test when it declines to compact.
func prepare(t *testing.T, build *chain, settings compact.Settings) *compact.Preparation {
	t.Helper()
	preparation, err := compact.Prepare(build.entries(), settings)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if preparation == nil {
		t.Fatalf("prepare: got nothing to compact")
	}
	return preparation
}

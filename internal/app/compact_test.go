package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/compact"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// smallCompaction keeps the last few messages so a short seeded conversation has
// a range to summarize.
func smallCompaction() *compact.Settings {
	return &compact.Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 2000}
}

// seedHistory appends count assistant turns of roughly one thousand tokens each.
func seedHistory(t *testing.T, manager *session.Manager, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := manager.AppendMessage(wire.UserMessage(strings.Repeat("x", 4000))); err != nil {
			t.Fatalf("append prompt: %v", err)
		}
		if _, err := manager.AppendMessage(wire.AssistantMessage(wire.TextContent(strings.Repeat("y", 4000)))); err != nil {
			t.Fatalf("append answer: %v", err)
		}
	}
}

// collectEvents returns an emitter and the events it received.
func collectEvents() (func(wire.AgentEvent) error, *[]wire.AgentEvent) {
	events := &[]wire.AgentEvent{}
	return func(event wire.AgentEvent) error {
		*events = append(*events, event)
		return nil
	}, events
}

func TestCompactAppendsACheckpoint(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := faux.New(faux.Response{Text: "HISTORY", Usage: wire.Usage{Input: 10, Output: 5, TotalTokens: 15}})
	application := newTestApp(t, manager, prov, func(cfg *Config) { cfg.Compaction = smallCompaction() })
	emit, events := collectEvents()

	result, err := application.Compact(context.Background(), "", emit)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if result.Summary != "HISTORY" {
		t.Fatalf("summary: %q, want the history summary alone", result.Summary)
	}
	if result.TokensBefore < 5000 {
		t.Fatalf("tokens before: got %d, want about the seeded 6000", result.TokensBefore)
	}
	if result.EstimatedTokensAfter == nil || *result.EstimatedTokensAfter <= 0 || *result.EstimatedTokensAfter >= result.TokensBefore {
		t.Fatalf("estimated tokens after: %v", result.EstimatedTokensAfter)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage: %+v", result.Usage)
	}

	path := manager.Path("")
	entry := path[len(path)-1]
	if entry.Type != session.TypeCompaction {
		t.Fatalf("tip: got %q want a compaction", entry.Type)
	}
	if entry.Summary != result.Summary || entry.FirstKeptEntryID != result.FirstKeptEntryID {
		t.Fatalf("entry: %+v", entry)
	}
	if string(entry.Details) != `{"readFiles":[],"modifiedFiles":[]}` {
		t.Fatalf("details: %s", entry.Details)
	}

	// The transcript is rebuilt from the checkpoint, so the summarized range is
	// gone from what the next request sends.
	if got := application.MessageCount(); got != 3 {
		t.Fatalf("messages: got %d want the checkpoint plus the kept turn", got)
	}
	if application.Busy() {
		t.Fatal("the app stayed busy after a compaction")
	}

	if len(*events) != 2 {
		t.Fatalf("events: got %d want start and end", len(*events))
	}
	start, end := (*events)[0], (*events)[1]
	if start.Type != wire.AgentEventCompactionStart || start.Reason != wire.CompactionReasonManual {
		t.Fatalf("start event: %+v", start)
	}
	if end.Type != wire.AgentEventCompactionEnd || end.Reason != wire.CompactionReasonManual {
		t.Fatalf("end event: %+v", end)
	}
	if end.Aborted == nil || *end.Aborted || end.WillRetry == nil || *end.WillRetry {
		t.Fatalf("end event flags: %+v", end)
	}
	if end.ErrorMessage != "" || len(end.Result) == 0 {
		t.Fatalf("end event payload: %+v", end)
	}
	var payload wire.CompactionResult
	if err := json.Unmarshal(end.Result, &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if payload.Summary != result.Summary || payload.FirstKeptEntryID != result.FirstKeptEntryID {
		t.Fatalf("payload: %+v", payload)
	}
}

func TestCompactRejectsASessionWithNothingToSummarize(t *testing.T) {
	manager := newSession(t)
	prov := faux.New()
	application := newTestApp(t, manager, prov)
	emit, events := collectEvents()

	if _, err := application.Compact(context.Background(), "", emit); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err: got %v want ErrNothingToCompact", err)
	}
	if application.Busy() {
		t.Fatal("a failed compaction left the app busy")
	}
	if len(*events) != 2 {
		t.Fatalf("events: got %d want start and end", len(*events))
	}
	end := (*events)[1]
	if end.Type != wire.AgentEventCompactionEnd || end.ErrorMessage != "Compaction failed: "+ErrNothingToCompact.Error() {
		t.Fatalf("end event: %+v", end)
	}
	if end.Aborted == nil || *end.Aborted {
		t.Fatalf("end event: %+v, want a failure rather than an abort", end)
	}
	if len(end.Result) != 0 {
		t.Fatalf("end event carried a result: %s", end.Result)
	}
	if len(manager.Path("")) != 0 {
		t.Fatalf("entries: got %d want none", len(manager.Path("")))
	}
}

func TestCompactRejectsAnAlreadyCompactedBranch(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := faux.New(faux.Response{Text: "HISTORY"}, faux.Response{Text: "PREFIX"})
	application := newTestApp(t, manager, prov, func(cfg *Config) { cfg.Compaction = smallCompaction() })

	if _, err := application.Compact(context.Background(), "", nil); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	emit, events := collectEvents()
	if _, err := application.Compact(context.Background(), "", emit); !errors.Is(err, ErrAlreadyCompacted) {
		t.Fatalf("err: got %v want ErrAlreadyCompacted", err)
	}
	if len(*events) != 2 {
		t.Fatalf("events: got %d want start and end", len(*events))
	}
	if end := (*events)[1]; end.ErrorMessage != "Compaction failed: "+ErrAlreadyCompacted.Error() {
		t.Fatalf("end event: %+v", end)
	}
}

// TestCompactKeepsTheSessionWhenTheSummaryFails pins that a partial summary
// never reaches the session: the branch stays exactly as it was.
func TestCompactKeepsTheSessionWhenTheSummaryFails(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	before := len(manager.Path(""))
	prov := faux.New(faux.Response{Text: "partial", StopReason: wire.StopLength})
	application := newTestApp(t, manager, prov, func(cfg *Config) { cfg.Compaction = smallCompaction() })
	messagesBefore := application.MessageCount()
	emit, events := collectEvents()

	if _, err := application.Compact(context.Background(), "", emit); err == nil {
		t.Fatal("compact: want a failure")
	}
	if got := len(manager.Path("")); got != before {
		t.Fatalf("entries: got %d want %d", got, before)
	}
	if got := application.MessageCount(); got != messagesBefore {
		t.Fatalf("messages: got %d want %d", got, messagesBefore)
	}
	end := (*events)[1]
	if !strings.HasPrefix(end.ErrorMessage, "Compaction failed: compact: summarize failed:") {
		t.Fatalf("end event: %+v", end)
	}
	if end.Aborted == nil || *end.Aborted {
		t.Fatalf("end event: %+v, want a failure rather than an abort", end)
	}
}

func TestCompactRejectsABusyConversation(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := newGateProvider()
	application := newTestApp(t, manager, prov, func(cfg *Config) { cfg.Compaction = smallCompaction() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = application.Ask(context.Background(), wire.UserMessage("hi"), nil)
	}()
	<-prov.started

	if _, err := application.Compact(context.Background(), "", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("err: got %v want ErrBusy", err)
	}
	// The turn is still running, so the rejection did not clear its flag.
	if !application.Busy() {
		t.Fatal("the running turn lost its busy flag")
	}

	// Let the turn finish before the temporary session directory is removed:
	// the cleanup would otherwise race with a writer that is still appending.
	close(prov.release)
	<-done
}

// TestAskCompactsBeforeThePrompt pins the admission path: a branch that is
// already past the threshold is summarized before the prompt travels, so the
// checkpoint sits ahead of the turn rather than at the tip.
func TestAskCompactsBeforeThePrompt(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	// No split-turn prefix is asked for: the prompt that would start the turn
	// has not been appended yet, so the cut lands on a whole seeded turn.
	prov := faux.New(
		faux.Response{Text: "HISTORY"},
		faux.Response{Text: "answer"},
	)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 6500
		cfg.Compaction = smallCompaction()
	})
	emit, events := collectEvents()

	result, err := application.Ask(context.Background(), wire.UserMessage("go on"), emit)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Text() != "answer" {
		t.Fatalf("answer: %q", result.Text())
	}

	path := manager.Path("")
	checkpoints := 0
	for _, entry := range path {
		if entry.Type == session.TypeCompaction {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoints: got %d want exactly one", checkpoints)
	}
	if tip := path[len(path)-1]; tip.Type != session.TypeMessage {
		t.Fatalf("tip: got %q want the answer, because the checkpoint came first", tip.Type)
	}
	var reasons []string
	for _, event := range *events {
		if event.Type == wire.AgentEventCompactionStart {
			reasons = append(reasons, event.Reason)
		}
	}
	if len(reasons) != 1 || reasons[0] != wire.CompactionReasonThreshold {
		t.Fatalf("compaction reasons: %v want one threshold compaction", reasons)
	}
}

// TestAskCompactsAfterATurnThatFillsTheContext pins the other ordering: when the
// answer itself is what crosses the threshold, admission had nothing to do and
// the checkpoint lands at the tip.
func TestAskCompactsAfterATurnThatFillsTheContext(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(
		faux.Response{Text: strings.Repeat("y", 24000)},
		faux.Response{Text: "HISTORY"},
		faux.Response{Text: "PREFIX"},
	)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 6500
		cfg.Compaction = smallCompaction()
	})
	emit, events := collectEvents()

	if _, err := application.Ask(context.Background(), wire.UserMessage("go on"), emit); err != nil {
		t.Fatalf("ask: %v", err)
	}
	path := manager.Path("")
	if tip := path[len(path)-1]; tip.Type != session.TypeCompaction {
		t.Fatalf("tip: got %q want a checkpoint after the turn", tip.Type)
	}
	var reasons []string
	for _, event := range *events {
		if event.Type == wire.AgentEventCompactionStart {
			reasons = append(reasons, event.Reason)
		}
	}
	if len(reasons) != 1 || reasons[0] != wire.CompactionReasonThreshold {
		t.Fatalf("compaction reasons: %v want one threshold compaction", reasons)
	}
}

// TestAskIgnoresTheThresholdWhenCompactionIsOff pins that disabling compaction
// is not a fallback to the defaults.
func TestAskIgnoresTheThresholdWhenCompactionIsOff(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := faux.New(faux.Response{Text: "answer"})
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 4000
		cfg.Compaction = &compact.Settings{Enabled: false, ReserveTokens: 1000, KeepRecentTokens: 2000}
	})
	emit, events := collectEvents()

	if _, err := application.Ask(context.Background(), wire.UserMessage("go on"), emit); err != nil {
		t.Fatalf("ask: %v", err)
	}
	for _, event := range *events {
		if event.Type == wire.AgentEventCompactionStart {
			t.Fatalf("compaction ran while disabled: %+v", event)
		}
	}
	if prov.Pending() != 0 {
		t.Fatalf("responses left: %d, want the turn to use the only one", prov.Pending())
	}
}

// TestAskKeepsTheAnswerWhenAutoCompactionFails pins that a compaction problem
// does not turn a completed turn into a failure.
func TestAskKeepsTheAnswerWhenAutoCompactionFails(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	// Both passes hit the same wall: admission before the prompt and the
	// post-turn check after it. Neither may cost the turn its answer. The window
	// is chosen so the seeded history is close to full rather than over it: a
	// request that still fits is sent, even when the summary it wanted failed.
	prov := faux.New(
		faux.Response{Text: "partial", StopReason: wire.StopLength},
		faux.Response{Text: "answer"},
		faux.Response{Text: "partial", StopReason: wire.StopLength},
	)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 6500
		cfg.Compaction = smallCompaction()
	})
	emit, events := collectEvents()

	result, err := application.Ask(context.Background(), wire.UserMessage("go on"), emit)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Text() != "answer" {
		t.Fatalf("answer: %q", result.Text())
	}
	var failures []wire.AgentEvent
	for _, event := range *events {
		if event.Type == wire.AgentEventCompactionEnd {
			failures = append(failures, event)
		}
	}
	if len(failures) != 2 || !strings.HasPrefix(failures[0].ErrorMessage, "Auto-compaction failed:") {
		t.Fatalf("compaction end: %+v", failures)
	}
	path := manager.Path("")
	if tip := path[len(path)-1]; tip.Type == session.TypeCompaction {
		t.Fatal("a failed summary was recorded anyway")
	}
}

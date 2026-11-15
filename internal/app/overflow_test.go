package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/compact"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// overflowResponse is the wording anthropic uses, which the shared classifier
// recognizes.
func overflowResponse() faux.Response {
	return faux.Response{Err: errors.New("400 Bad Request: prompt is too long: 900000 tokens > 200000 maximum")}
}

// overflowEvents splits a recorded event stream by type.
func overflowEvents(events []wire.AgentEvent) (starts, ends []wire.AgentEvent) {
	for _, event := range events {
		switch event.Type {
		case wire.AgentEventCompactionStart:
			starts = append(starts, event)
		case wire.AgentEventCompactionEnd:
			ends = append(ends, event)
		}
	}
	return starts, ends
}

// TestAskRecoversFromOverflow walks the whole recovery: the request is refused by
// the vendor as too large, the failed attempt is set aside, the branch is
// summarized, and the turn is asked once more.
func TestAskRecoversFromOverflow(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := faux.New(
		overflowResponse(),
		faux.Response{Text: "HISTORY"},
		faux.Response{Text: "recovered"},
	)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 200000
		cfg.Compaction = smallCompaction()
	})
	emit, events := collectEvents()

	result, err := application.Ask(context.Background(), wire.UserMessage("go on"), emit)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Text() != "recovered" {
		t.Fatalf("answer: %q want the retried turn", result.Text())
	}
	if result.StopReason != wire.StopStop {
		t.Fatalf("stop reason: %q", result.StopReason)
	}
	// Three requests: the failed turn, the summary, the retry.
	if left := prov.Pending(); left != 0 {
		t.Fatalf("responses left: %d, want exactly three requests", left)
	}

	starts, ends := overflowEvents(*events)
	if len(starts) != 1 || starts[0].Reason != wire.CompactionReasonOverflow {
		t.Fatalf("compaction starts: %+v", starts)
	}
	if len(ends) != 1 {
		t.Fatalf("compaction ends: %+v", ends)
	}
	if ends[0].WillRetry == nil || !*ends[0].WillRetry {
		t.Fatalf("willRetry: %+v want true", ends[0])
	}

	// The failed attempt is out of the context but still in the file, named by a
	// context edit. Reopening is the check that matters: the edit has to survive
	// the round trip through JSON, or a resumed session would put the failed
	// attempt back in front of the model.
	for _, message := range application.Messages() {
		if message.StopReason == wire.StopError {
			t.Fatalf("the failed attempt is still in the context: %+v", message)
		}
	}
	file := manager.File()
	reopened, err := session.Open(file, "", "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	edits := 0
	for _, entry := range reopened.Path("") {
		if entry.Type == session.TypeContextEdit {
			edits++
			if string(entry.Replacement) != "null" {
				t.Fatalf("context edit replacement: %q want an explicit null", entry.Replacement)
			}
		}
	}
	if edits != 1 {
		t.Fatalf("context edits: got %d want one", edits)
	}
	context, err := reopened.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	for _, message := range context.Messages {
		if message.StopReason == wire.StopError {
			t.Fatalf("the failed attempt came back after a reopen: %+v", message)
		}
	}
}

// TestAskGivesUpAfterOneOverflowRetry pins the budget: one compact-and-retry, then
// a sentence a reader can act on instead of a second identical request.
func TestAskGivesUpAfterOneOverflowRetry(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := faux.New(
		overflowResponse(),
		faux.Response{Text: "HISTORY"},
		overflowResponse(),
	)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 200000
		cfg.Compaction = smallCompaction()
	})
	emit, events := collectEvents()

	result, err := application.Ask(context.Background(), wire.UserMessage("go on"), emit)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if left := prov.Pending(); left != 0 {
		t.Fatalf("responses left: %d, want no second retry", left)
	}
	if !strings.HasPrefix(result.ErrorMessage, "Context overflow:") {
		t.Fatalf("error message: %q want the actionable sentence", result.ErrorMessage)
	}
	if !strings.Contains(result.ErrorMessage, "200000-token window") {
		t.Fatalf("error message: %q want the window", result.ErrorMessage)
	}

	_, ends := overflowEvents(*events)
	if len(ends) != 2 {
		t.Fatalf("compaction ends: %+v want the summary and the give-up", ends)
	}
	if ends[0].WillRetry == nil || !*ends[0].WillRetry {
		t.Fatalf("first willRetry: %+v want true", ends[0])
	}
	if ends[1].WillRetry == nil || *ends[1].WillRetry {
		t.Fatalf("second willRetry: %+v want false", ends[1])
	}
	if !strings.HasPrefix(ends[1].ErrorMessage, "Context overflow:") {
		t.Fatalf("second end: %+v", ends[1])
	}
}

// TestAskRefusesAPromptThatCannotFit is the admission check at its bluntest: a
// prompt larger than the window with nothing to summarize behind it is not sent
// at all.
func TestAskRefusesAPromptThatCannotFit(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "never"})
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 1000
		cfg.Compaction = smallCompaction()
	})

	_, err := application.Ask(context.Background(), wire.UserMessage(strings.Repeat("x", 8000)), nil)
	if err == nil {
		t.Fatal("the oversized prompt was sent")
	}
	if !strings.HasPrefix(err.Error(), "Context overflow:") {
		t.Fatalf("error: %v", err)
	}
	// There was nothing to summarize, which is a fact about the session and not
	// a reason to hand the reader.
	if strings.Contains(err.Error(), "nothing to compact") {
		t.Fatalf("error leaks an internal reason: %v", err)
	}
	if calls := prov.Calls(); len(calls) != 0 {
		t.Fatalf("requests sent: %d, want none", len(calls))
	}
}

// TestAskRefusesWhenTheSummaryFailsOnTheWay covers the other way to stay too
// large: compaction is possible but fails, so the request that follows would be
// the same one that did not fit.
func TestAskRefusesWhenTheSummaryFailsOnTheWay(t *testing.T) {
	manager := newSession(t)
	seedHistory(t, manager, 3)
	prov := faux.New(faux.Response{Text: "partial", StopReason: wire.StopLength})
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 6500
		cfg.Compaction = smallCompaction()
	})

	// 1000 tokens of prompt on top of 6000 tokens of history: past the window,
	// with plenty to summarize — but the summary fails.
	_, err := application.Ask(context.Background(), wire.UserMessage(strings.Repeat("x", 4000)), nil)
	if err == nil {
		t.Fatal("the turn was sent anyway")
	}
	if !strings.Contains(err.Error(), "summarizing the branch failed") {
		t.Fatalf("error: %v", err)
	}
	if calls := prov.Calls(); len(calls) != 1 {
		t.Fatalf("requests: got %d want only the summary attempt", len(calls))
	}
}

// TestAskSendsWhenCompactionIsOff pins that switching automatic compaction off
// switches overflow recovery and admission off with it: the user took the wheel.
func TestAskSendsWhenCompactionIsOff(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "sent anyway"})
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Model.ContextWindow = 1000
		cfg.Compaction = &compact.Settings{Enabled: false}
	})
	emit, events := collectEvents()

	result, err := application.Ask(context.Background(), wire.UserMessage(strings.Repeat("x", 8000)), emit)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Text() != "sent anyway" {
		t.Fatalf("answer: %q", result.Text())
	}
	if starts, _ := overflowEvents(*events); len(starts) != 0 {
		t.Fatalf("compacted while disabled: %+v", starts)
	}
}

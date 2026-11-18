package webhost

import (
	"context"
	"strings"
	"testing"

	"pi-go/internal/httpapi"
	"pi-go/internal/wire"
)

// The snapshot the side conversation inherits is drawn, not hidden: a reader who
// asks the panel "what did we just say" sees the same words the model was given.
func TestOpenSideInheritsTheConversationInView(t *testing.T) {
	host, _ := forkKernel(t)

	if view, err := host.Side(); err != nil || view.Open {
		t.Fatalf("side before opening = %+v, %v", view, err)
	}
	if err := host.OpenSide(); err != nil {
		t.Fatalf("open: %v", err)
	}
	view, err := host.Side()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !view.Open {
		t.Fatal("side is not open after opening it")
	}
	// Inherited is counted in blocks, because a divider placed by message count
	// would land in the middle of a tool card. It is also exactly the whole
	// conversation at this point: nothing has been asked in it yet.
	if view.Inherited == 0 || view.Inherited != len(view.Blocks) {
		t.Fatalf("inherited = %d of %d blocks, want all of them", view.Inherited, len(view.Blocks))
	}
	var said []string
	for _, block := range view.Blocks {
		said = append(said, block.Role+":"+block.Text)
	}
	if !strings.Contains(strings.Join(said, "|"), "user:hi") {
		t.Fatalf("blocks = %v, want the prompt the conversation was started with", said)
	}
}

// Asking again is a redraw, not a reset. BeginSide replaces what is open — that
// is what makes /side a way to start over in the terminal — and the guard here is
// what keeps a page that repaints from throwing away what the reader was reading.
func TestOpeningTwiceKeepsWhatWasSaidInIt(t *testing.T) {
	host, _ := forkKernel(t)
	if err := host.OpenSide(); err != nil {
		t.Fatalf("open: %v", err)
	}
	side := host.kernel.App.Side()
	if side == nil {
		t.Fatal("no side conversation in the kernel")
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("apart"), nil); err != nil {
		t.Fatalf("ask in side: %v", err)
	}

	if err := host.OpenSide(); err != nil {
		t.Fatalf("open again: %v", err)
	}
	view, err := host.Side()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(textOf(view), "apart") {
		t.Fatalf("side = %q, want the question that was already asked in it", textOf(view))
	}
}

// The prefix count is the panel's divider, so it has to survive the reload the
// page cannot survive: the conversation lives in memory and the page does not.
func TestTheInheritedPrefixSurvivesAReopen(t *testing.T) {
	host, _ := forkKernel(t)
	if err := host.OpenSide(); err != nil {
		t.Fatalf("open: %v", err)
	}
	before, err := host.Side()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// A page that reloads asks again without meaning to start over; the guard
	// makes that a read.
	if err := host.OpenSide(); err != nil {
		t.Fatalf("open again: %v", err)
	}
	after, err := host.Side()
	if err != nil {
		t.Fatalf("read again: %v", err)
	}
	if after.Inherited != before.Inherited {
		t.Fatalf("inherited %d after a reopen, was %d", after.Inherited, before.Inherited)
	}
}

// Closing leaves nothing behind for the next open to inherit by accident.
func TestClosingForgetsThePrefix(t *testing.T) {
	host, _ := forkKernel(t)
	if err := host.OpenSide(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := host.CloseSide(); err != nil {
		t.Fatalf("close: %v", err)
	}
	view, err := host.Side()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if view.Open || view.Inherited != 0 || len(view.Blocks) != 0 {
		t.Fatalf("side = %+v, want nothing left", view)
	}
	// An empty conversation is a list, not null: the panel folds the answer.
	if view.Blocks == nil {
		t.Fatal("blocks = null, want an empty list")
	}
}

// A compaction of a conversation with nothing to trade says so instead of
// reporting a success that shrank nothing. The events are how the window finds
// out: compaction_end carries the message, and the caller gets the error that
// becomes a 409.
func TestCompactReportsThatThereIsNothingToSummarize(t *testing.T) {
	host, _ := forkKernel(t)

	var events []wire.AgentEvent
	err := host.Compact(context.Background(), "", func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err == nil {
		t.Fatal("compaction of a two-message conversation reported success")
	}
	if len(events) < 2 {
		t.Fatalf("events = %+v, want a start and an end", events)
	}
	if events[0].Type != wire.AgentEventCompactionStart || events[0].Reason != wire.CompactionReasonManual {
		t.Fatalf("first event = %+v, want the manual start", events[0])
	}
	last := events[len(events)-1]
	if last.Type != wire.AgentEventCompactionEnd || last.ErrorMessage == "" {
		t.Fatalf("last event = %+v, want a failure carrying its reason", last)
	}
}

// textOf joins everything the panel would draw into one string, which is enough
// to ask "is this sentence in there" without a matcher per role.
func textOf(view httpapi.SideView) string {
	parts := make([]string, 0, len(view.Blocks))
	for _, block := range view.Blocks {
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

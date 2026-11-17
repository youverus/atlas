package transcript

// The model's own invariants, tested without a screen attached. Rendering tests
// live in internal/tui, where there are cells to measure; what is pinned here is
// what any second frontend would be relying on: which blocks group into one
// stretch, what folding means, how much history a window reveals, and the role
// names that leave this package as data.

import (
	"testing"
	"time"
)

func TestSpansPartitionBlocks(t *testing.T) {
	var tr Transcript
	tr.Now = func() time.Time { return time.Unix(0, 0) }
	roles := []Role{RoleUser, RoleAnswer, RoleThinking, RoleThinking, RoleTool, RoleTool, RoleTool, RoleNotice}
	for _, role := range roles {
		tr.Append(Block{Role: role})
	}

	spans := tr.Spans()
	covered := 0
	for i, span := range spans {
		if span.First != covered {
			t.Fatalf("span %d starts at %d, want %d", i, span.First, covered)
		}
		if span.Last < span.First {
			t.Fatalf("span %d has Last %d before First %d", i, span.Last, span.First)
		}
		if i > 0 {
			previous := spans[i-1]
			if span.First != previous.Last+1 {
				t.Fatalf("span %d starts at %d, previous ends at %d", i, span.First, previous.Last)
			}
		}
		covered = span.Last + 1
	}
	if covered != len(roles) {
		t.Fatalf("spans cover %d blocks, want %d", covered, len(roles))
	}

	// Four stretches: prompt, answer, one run of reasoning, one run of calls,
	// and a notice. Reasoning and calls are the only kinds that merge.
	if len(spans) != 5 {
		t.Fatalf("spans = %d, want 5", len(spans))
	}
	if spans[2].First != 2 || spans[2].Last != 3 {
		t.Fatalf("reasoning span = %d..%d, want 2..3", spans[2].First, spans[2].Last)
	}
	if spans[3].First != 4 || spans[3].Last != 6 {
		t.Fatalf("calls span = %d..%d, want 4..6", spans[3].First, spans[3].Last)
	}
}

func TestNonFramedRolesNeverMerge(t *testing.T) {
	var tr Transcript
	tr.Now = func() time.Time { return time.Unix(0, 0) }
	tr.Append(Block{Role: RoleAnswer})
	tr.Append(Block{Role: RoleAnswer})
	tr.Append(Block{Role: RoleUser})
	tr.Append(Block{Role: RoleUser})
	if got := len(tr.Spans()); got != 4 {
		t.Fatalf("spans = %d, want 4", got)
	}
	for i := range tr.Spans() {
		if tr.ToggleSpan(i) {
			t.Fatalf("span %d is not foldable but ToggleSpan reported it was", i)
		}
	}
}

func TestToggleAllResolvesInsteadOfAlternating(t *testing.T) {
	var tr Transcript
	tr.Now = func() time.Time { return time.Unix(0, 0) }
	tr.Append(Block{Role: RoleTool})
	tr.Append(Block{Role: RoleThinking})

	// One stretch folded by hand: the next ToggleAll must show the open shape
	// rather than fold the other one too.
	if !tr.ToggleSpan(0) {
		t.Fatal("ToggleSpan(0) = false, want true")
	}
	if !tr.ToggleAll() {
		t.Fatal("ToggleAll() = false, want true")
	}
	for i, span := range tr.Spans() {
		if span.Collapsed {
			t.Fatalf("span %d still collapsed after ToggleAll, want every stretch open", i)
		}
	}

	// From fully open it folds everything, and from there it unfolds again.
	tr.ToggleAll()
	for i, span := range tr.Spans() {
		if !span.Collapsed {
			t.Fatalf("span %d open after ToggleAll, want every stretch folded", i)
		}
	}
	tr.ToggleAll()
	for i, span := range tr.Spans() {
		if span.Collapsed {
			t.Fatalf("span %d collapsed after the second ToggleAll", i)
		}
	}
}

func TestCloseStampsTheElapsedTime(t *testing.T) {
	base := time.Unix(1000, 0)
	now := base
	var tr Transcript
	tr.Now = func() time.Time { return now }

	index := tr.Append(Block{Role: RoleThinking, Open: true})
	now = base.Add(1200 * time.Millisecond)
	tr.Close(index, "Thought for")

	block := tr.Blocks()[index]
	if block.Open {
		t.Fatal("block still open after Close")
	}
	if want := "Thought for 1.2s"; block.Note != want {
		t.Fatalf("Note = %q, want %q", block.Note, want)
	}
}

func TestRevealWindowArithmetic(t *testing.T) {
	var tr Transcript
	tr.Now = func() time.Time { return time.Unix(0, 0) }
	for i := 0; i < 10; i++ {
		tr.Append(Block{Role: RoleUser})
	}
	if got := tr.Hidden(); got != 0 {
		t.Fatalf("Hidden() = %d on a fresh transcript, want 0", got)
	}

	// TrimAnswer keeps the newest answer drawn; with no answer at all the
	// window opens completely rather than hiding everything.
	tr.TrimAnswer()
	if got := tr.Hidden(); got != 0 {
		t.Fatalf("Hidden() = %d with no answer, want 0", got)
	}

	answered := New()
	answered.Now = func() time.Time { return time.Unix(0, 0) }
	for i := 0; i < 4; i++ {
		answered.Append(Block{Role: RoleUser})
	}
	answered.Append(Block{Role: RoleAnswer})
	answered.Append(Block{Role: RoleUser})
	answered.TrimAnswer()
	if got := answered.Hidden(); got != 4 {
		t.Fatalf("Hidden() = %d, want 4 (the answer is the fifth stretch)", got)
	}

	answered.Reveal(2)
	if got := answered.Hidden(); got != 2 {
		t.Fatalf("Hidden() = %d after Reveal(2), want 2", got)
	}
	// Revealing past the start clamps instead of going negative.
	answered.Reveal(99)
	if got := answered.Hidden(); got != 0 {
		t.Fatalf("Hidden() = %d after Reveal(99), want 0", got)
	}
	// A reveal of nothing is not an error and does not move the window.
	answered.Reveal(0)
	if got := answered.Hidden(); got != 0 {
		t.Fatalf("Hidden() = %d after Reveal(0), want 0", got)
	}
}

func TestResetKeepsTheClock(t *testing.T) {
	clock := func() time.Time { return time.Unix(42, 0) }
	var tr Transcript
	tr.Now = clock
	tr.Append(Block{Role: RoleUser})
	tr.TrimAnswer()
	tr.Reset()

	if tr.Len() != 0 || len(tr.Spans()) != 0 || tr.Hidden() != 0 {
		t.Fatalf("Reset left state behind: len=%d spans=%d hidden=%d", tr.Len(), len(tr.Spans()), tr.Hidden())
	}
	if tr.Now == nil {
		t.Fatal("Reset cleared the clock, which is configuration and not content")
	}
}

func TestRoleNames(t *testing.T) {
	// These names are the JSON-facing contract: a frontend keys on them, so
	// changing one is a protocol change and this test is where it is noticed.
	want := map[Role]string{
		RoleUser:     "user",
		RoleAnswer:   "answer",
		RoleThinking: "thinking",
		RoleTool:     "tool",
		RoleNotice:   "notice",
		RoleError:    "error",
	}
	for role, name := range want {
		if got := role.String(); got != name {
			t.Fatalf("Role(%d).String() = %q, want %q", role, got, name)
		}
	}
	if got := Role(99).String(); got != "unknown" {
		t.Fatalf("Role(99).String() = %q, want %q", got, "unknown")
	}
}

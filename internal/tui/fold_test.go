package tui

// 0.12.15 lets a reader fold a stretch. The rule the tests below hold is that
// folding is the only difference: a folded stretch is the title its box already
// carried, the rows around it do not move, and with the boxes switched off there
// is nothing to fold at all.

import (
	"strings"
	"testing"
)

// TestFoldingAStretchKeepsItsTitle: a folded stretch draws one line — the marker
// and the title — and the title is the one the box's top rule had, because a
// reader who folds a stretch is asking for less of it and not for a different
// name.
func TestFoldingAStretchKeepsItsTitle(t *testing.T) {
	const width = 80
	tr := alternatingTurn()
	layout := DefaultLayout()
	before := tr.Lines(width, testStyles(t), layout)
	if !tr.ToggleSpan(1) {
		t.Fatalf("the reasoning stretch cannot be folded: %v", tr.Spans())
	}
	after := tr.Lines(width, testStyles(t), layout)

	// One box became one line: the box's rule, its body and its closing rule
	// are gone, and the folded line took their place.
	body := before[2] // the prompt, then the blank line, then the first box
	if !strings.Contains(body, "Thought for 3.1s") {
		t.Fatalf("box 1 is titled %q", body)
	}
	if len(after) >= len(before) {
		t.Fatalf("folding drew %d rows, want fewer than the %d before it:\n%s", len(after), len(before), strings.Join(after, "\n"))
	}
	folded := ""
	for _, line := range after {
		if strings.Contains(line, "Thought for 3.1s") {
			folded = line
		}
	}
	if !strings.Contains(folded, foldedMark) {
		t.Fatalf("the folded stretch is %q, want the marker and its title", folded)
	}
	if strings.Contains(strings.Join(after, "\n"), "It builds every package.") {
		t.Fatalf("the folded stretch kept its body:\n%s", strings.Join(after, "\n"))
	}
	// The other stretches are untouched: three boxes are left.
	if spans := boxes(after); len(spans) != 3 {
		t.Fatalf("folding one box left %d boxes, want three:\n%s", len(spans), strings.Join(after, "\n"))
	}
}

// TestFoldingOneStretchLeavesTheRestPinned: the rows above and below the fold are
// the rows that were there. A fold that moved anything else would make the
// transcript jump under the reader's eyes, which is the one thing a fold is
// meant to save them from.
func TestFoldingOneStretchLeavesTheRestPinned(t *testing.T) {
	const width = 80
	tr := alternatingTurn()
	layout := DefaultLayout()
	before := tr.Lines(width, testStyles(t), layout)
	if !tr.ToggleSpan(1) {
		t.Fatal("the reasoning stretch cannot be folded")
	}
	after := tr.Lines(width, testStyles(t), layout)

	// The prompt is above the fold and the answer below it.
	if after[0] != before[0] {
		t.Fatalf("the prompt moved: %q, was %q", after[0], before[0])
	}
	if got, want := after[len(after)-1], before[len(before)-1]; got != want {
		t.Fatalf("the answer moved: %q, was %q", got, want)
	}
	// And the fold took exactly the box's rows, minus the one line left over.
	box := boxes(before)[0]
	if rows := box[1] - box[0]; len(before)-len(after) != rows {
		t.Fatalf("folding the %d-row box took %d rows", rows+1, len(before)-len(after))
	}
}

// TestOnlyStretchesFold: a prompt, an answer and a notice are one row of their
// own kind already. Folding one of them would hide a sentence the reader wrote
// behind a marker saying it was there, which is not what the gesture means.
func TestOnlyStretchesFold(t *testing.T) {
	tr := alternatingTurn()
	for _, index := range []int{0, 5} {
		if tr.ToggleSpan(index) {
			t.Fatalf("span %d is %v and can be folded", index, tr.Spans()[index])
		}
	}
	for _, index := range []int{-1, len(tr.Spans())} {
		if tr.ToggleSpan(index) {
			t.Fatalf("span %d is out of range and can be folded", index)
		}
	}
}

// TestToggleAllFoldsEveryStretchThenUnfoldsThem: the keyboard gesture answers
// "show me the shape of this turn" once, and the same gesture puts the detail
// back. Half-folded is a state it never leaves behind, which is what makes the
// key a toggle rather than a step.
func TestToggleAllFoldsEveryStretchThenUnfoldsThem(t *testing.T) {
	tr := alternatingTurn()
	if !tr.ToggleAll() {
		t.Fatal("a turn of four stretches cannot be folded at all")
	}
	for _, span := range tr.Spans() {
		if framed(tr.Blocks()[span.First].Role) && !span.Collapsed {
			t.Fatalf("span %v was left unfolded", span)
		}
	}
	if !tr.ToggleAll() {
		t.Fatal("the second press did nothing")
	}
	for _, span := range tr.Spans() {
		if span.Collapsed {
			t.Fatalf("span %v was left folded", span)
		}
	}
}

// TestToggleAllUnfoldsRatherThanFoldingHalf: with some stretches already folded a
// click may have left the transcript neither open nor closed, and the key that
// follows has to resolve it rather than alternate. One press opens everything.
func TestToggleAllUnfoldsRatherThanFoldingHalf(t *testing.T) {
	tr := alternatingTurn()
	if !tr.ToggleSpan(1) {
		t.Fatal("the reasoning stretch cannot be folded")
	}
	if !tr.ToggleAll() {
		t.Fatal("the key did nothing")
	}
	for _, span := range tr.Spans() {
		if span.Collapsed {
			t.Fatalf("span %v stayed folded: a half-folded transcript should open", span)
		}
	}
}

// TestFramesOffLeavesNothingToFold: without boxes there is no title to fold to,
// so a stretch folded while the boxes were on is drawn as the blocks it always
// was. The switch stays the whole truth about the shape, which is what keeps
// "off is the previous version" checkable.
func TestFramesOffLeavesNothingToFold(t *testing.T) {
	tr := alternatingTurn()
	layout := DefaultLayout()
	layout.TranscriptFrames = false
	before := tr.Lines(80, testStyles(t), layout)
	if !tr.ToggleSpan(1) {
		t.Fatal("the reasoning stretch cannot be folded")
	}
	after := tr.Lines(80, testStyles(t), layout)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("folding changed the frameless transcript:\n%s\n---\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

// TestAFoldSurvivesNewBlocks: the state lives on the stretch, and appending more
// of the same kind extends that stretch rather than starting a new one. A turn
// that keeps thinking after the reader folded it must not unfold itself.
func TestAFoldSurvivesNewBlocks(t *testing.T) {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleThinking, Text: "one", Note: "Thought for 1s"})
	if !tr.ToggleSpan(0) {
		t.Fatal("the reasoning stretch cannot be folded")
	}
	tr.Append(Block{Role: RoleThinking, Text: "two"})
	if spans := tr.Spans(); len(spans) != 1 || !spans[0].Collapsed {
		t.Fatalf("spans = %v, want one folded stretch", tr.Spans())
	}
	body := strings.Join(tr.Lines(60, testStyles(t), DefaultLayout()), "\n")
	if !strings.Contains(body, foldedMark) || strings.Contains(body, "two") {
		t.Fatalf("the stretched unfolded itself:\n%s", body)
	}
}

// TestAFoldedLineFitsTheNarrowestScreen: the folded line is one row of whatever
// width the screen has. A title that overflows it would push the next row off,
// and the rows the fold just saved would be spent on the title.
func TestAFoldedLineFitsTheNarrowestScreen(t *testing.T) {
	tr := alternatingTurn()
	if !tr.ToggleSpan(2) {
		t.Fatal("the call stretch cannot be folded")
	}
	for _, width := range []int{80, 40, 20, 12, 4} {
		lines := tr.Lines(width, testStyles(t), DefaultLayout())
		if width < 4 {
			width = 4
		}
		folded := ""
		for _, line := range lines {
			if strings.Contains(line, foldedMark) {
				folded = line
			}
		}
		if folded == "" {
			t.Fatalf("width %d lost the folded line:\n%s", width, strings.Join(lines, "\n"))
		}
		if got := len([]rune(plain(folded))); got > width {
			t.Fatalf("width %d folded line is %d cells: %q", width, got, folded)
		}
	}
}

// TestTheFrameSaysWhichStretchDrewARow: the mapping is what a click is answered
// with, so it has to name the stretch and not merely some row inside one.
func TestTheFrameSaysWhichStretchDrewARow(t *testing.T) {
	tr := alternatingTurn()
	frame := tr.Frame(80, testStyles(t), DefaultLayout())
	if len(frame.Spans) != len(tr.Spans()) {
		t.Fatalf("frame has %d stretches, transcript has %d", len(frame.Spans), len(tr.Spans()))
	}
	for index, rows := range frame.Spans {
		if rows.First < 0 || rows.Last < rows.First || rows.Last >= len(frame.Lines) {
			t.Fatalf("stretch %d has rows %v of %d", index, rows, len(frame.Lines))
		}
		for row := rows.First; row <= rows.Last; row++ {
			if at, ok := frame.SpanAt(row); !ok || at != index {
				t.Fatalf("row %d belongs to stretch %d, not %d", row, at, index)
			}
		}
	}
	// The blank line between two stretches belongs to neither, which is what
	// makes a click on it a click on nothing rather than on its neighbour.
	if at, ok := frame.SpanAt(frame.Spans[0].Last + 1); ok {
		t.Fatalf("the separator belongs to stretch %d", at)
	}
}

package tui

// The selection is a range of cells, not a range of bytes, and these tests are
// about that difference: styled rows, wide graphemes, and the two ends of a drag
// that can land anywhere.

import (
	"strings"
	"testing"
)

// markedCells is the text between the highlight and the end of it, with the
// styling taken off: the words the reader would see selected.
func markedCells(t *testing.T, line string) string {
	t.Helper()
	on := strings.Index(line, reverseOn)
	if on < 0 {
		t.Fatalf("nothing was selected in %q", line)
	}
	rest := line[on+len(reverseOn):]
	off := strings.Index(rest, reverseOff)
	if off < 0 {
		t.Fatalf("the highlight was never closed in %q", line)
	}
	return plain(rest[:off])
}

// unmarked is the row with the highlight taken back out, which should be the row
// that went in.
func unmarked(line string) string {
	return strings.ReplaceAll(strings.ReplaceAll(line, reverseOn, ""), reverseOff, "")
}

func TestASelectionPaintsOnlyTheCellsItCovers(t *testing.T) {
	line := "abcdef"
	marked := markSelection(line, 1, 3)
	if got := markedCells(t, marked); got != "bc" {
		t.Fatalf("selected %q, want %q", got, "bc")
	}
	if strings.Count(marked, reverseOn) != 1 || strings.Count(marked, reverseOff) != 1 {
		t.Fatalf("the highlight was opened or closed more than once: %q", marked)
	}
	if unmarked(marked) != line {
		t.Fatalf("the row changed outside the selection: %q", unmarked(marked))
	}
}

// TestASelectionKeepsTheColourItLandedIn: cutting a styled row at two cells and
// painting the middle would drop the colour the cut landed in, so the highlight
// has to go around the cells rather than replace them.
func TestASelectionKeepsTheColourItLandedIn(t *testing.T) {
	line := "\x1b[31mabcdef\x1b[0m"
	marked := markSelection(line, 1, 3)
	if unmarked(marked) != line {
		t.Fatalf("the colour was lost: %q", unmarked(marked))
	}
	if got := markedCells(t, marked); got != "bc" {
		t.Fatalf("selected %q, want %q", got, "bc")
	}
	if strings.Index(marked, "\x1b[31m") > strings.Index(marked, reverseOn) {
		t.Fatalf("the highlight opened before the colour: %q", marked)
	}
}

// TestAWideGraphemeIsNeverSplit: a cell position inside a two-cell character is
// not a character boundary, so the highlight has to round to whichever
// characters the range actually covers.
func TestAWideGraphemeIsNeverSplit(t *testing.T) {
	line := "日本語"
	if got := markedCells(t, markSelection(line, 0, 2)); got != "日" {
		t.Fatalf("selected %q, want %q", got, "日")
	}
	if got := markedCells(t, markSelection(line, 1, 3)); got != "本" {
		t.Fatalf("selected %q, want %q", got, "本")
	}
	if got := markedCells(t, markSelection(line, 2, 5)); got != "本語" {
		t.Fatalf("selected %q, want %q", got, "本語")
	}
}

func TestASelectionThatRunsPastTheRowClosesItsHighlight(t *testing.T) {
	marked := markSelection("abcdef", 3, 99)
	if got := markedCells(t, marked); got != "def" {
		t.Fatalf("selected %q, want %q", got, "def")
	}
	if strings.Count(marked, reverseOn) != strings.Count(marked, reverseOff) {
		t.Fatalf("the highlight was left open: %q", marked)
	}
}

func TestAnEmptyRangePaintsNothing(t *testing.T) {
	line := "\x1b[31mabc\x1b[0m"
	for _, span := range [][2]int{{0, 0}, {2, 2}, {3, 1}} {
		if got := markSelection(line, span[0], span[1]); got != line {
			t.Fatalf("marking %v changed the row: %q", span, got)
		}
	}
}

// TestASelectionReadsInEitherDirection: a drag can go up and to the left, and
// the highlight does not care which end the mouse went down on.
func TestASelectionReadsInEitherDirection(t *testing.T) {
	down := point{row: 1, cell: 4}
	up := point{row: 3, cell: 1}
	away := selection{anchor: down, cursor: up}
	back := selection{anchor: up, cursor: down}
	wantFirst, wantLast := away.bounds()
	gotFirst, gotLast := back.bounds()
	if gotFirst != wantFirst || gotLast != wantLast {
		t.Fatalf("a drag up began at %v and ended at %v, the other way round at %v and %v",
			wantFirst, wantLast, gotFirst, gotLast)
	}
}

// TestTheMiddleRowsAreCoveredEndToEnd: only the rows the button was pressed and
// released on use the cell the mouse was over.
func TestTheMiddleRowsAreCoveredEndToEnd(t *testing.T) {
	sel := selection{anchor: point{row: 1, cell: 4}, cursor: point{row: 3, cell: 1}}
	cases := []struct {
		row, width int
		from, to   int
		ok         bool
	}{
		{row: 0, width: 20},
		{row: 1, width: 20, from: 4, to: 20, ok: true},
		{row: 2, width: 7, from: 0, to: 7, ok: true},
		{row: 3, width: 20, from: 0, to: 2, ok: true},
		{row: 4, width: 20},
	}
	for _, c := range cases {
		from, to, ok := sel.cells(c.row, c.width)
		if ok != c.ok || from != c.from || to != c.to {
			t.Fatalf("row %d of width %d gave [%d,%d) ok=%t, want [%d,%d) ok=%t",
				c.row, c.width, from, to, ok, c.from, c.to, c.ok)
		}
	}
}

// TestASelectionStopsAtTheEndOfAShortRow: a drag that overshoots a short row
// does not select the padding beside it.
func TestASelectionStopsAtTheEndOfAShortRow(t *testing.T) {
	sel := selection{anchor: point{row: 0, cell: 0}, cursor: point{row: 2, cell: 40}}
	if from, to, ok := sel.cells(1, 3); !ok || from != 0 || to != 3 {
		t.Fatalf("a short row gave [%d,%d) ok=%t, want [0,3) true", from, to, ok)
	}
	if _, _, ok := sel.cells(1, 0); ok {
		t.Fatal("a row of no width selected something")
	}
}

func TestAClickSelectsNothing(t *testing.T) {
	sel := selection{anchor: point{row: 2, cell: 3}, cursor: point{row: 2, cell: 3}}
	if !sel.empty() {
		t.Fatal("a press and release in one cell is not a drag")
	}
	if got := selectionText([]string{"one", "two"}, sel); got != "" {
		t.Fatalf("a click copied %q", got)
	}
}

// TestSelectedTextDropsThePaddingNotTheIndentation: a drag across a padded row
// should not paste its padding, and a drag that begins at the left margin should
// keep the indentation the line was written with.
func TestSelectedTextDropsThePaddingNotTheIndentation(t *testing.T) {
	line := "    four spaces and air        "
	if got := selectedText(line, 0, len(line)); got != "    four spaces and air" {
		t.Fatalf("copied %q", got)
	}
	if got := selectedText(line, 0, 8); got != "    four" {
		t.Fatalf("copied %q", got)
	}
	if got := selectedText("\x1b[31mred\x1b[0m words", 0, 9); got != "red words" {
		t.Fatalf("copied %q, want the words without the styling", got)
	}
}

// TestSelectionTextKeepsTheBlankLinesInsideIt: a paragraph with a gap in the
// middle keeps the gap, and the air the drag overshot onto at either end goes.
func TestSelectionTextKeepsTheBlankLinesInsideIt(t *testing.T) {
	lines := []string{"", "first", "", "second", "   ", ""}
	sel := selection{anchor: point{row: 0, cell: 0}, cursor: point{row: 5, cell: 3}}
	want := "first\n\nsecond"
	if got := selectionText(lines, sel); got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
}

// TestSelectionTextSkipsRowsThatAreNotThere: the transcript can shrink between
// the drag and the release, and the rows that went away are not copied as blanks
// in the middle of the text.
func TestSelectionTextSkipsRowsThatAreNotThere(t *testing.T) {
	lines := []string{"one", "two"}
	sel := selection{anchor: point{row: 0, cell: 0}, cursor: point{row: 9, cell: 3}}
	want := "one\ntwo"
	if got := selectionText(lines, sel); got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
}

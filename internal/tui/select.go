package tui

// The mouse is the second way into a transcript that is longer than the screen.
// The wheel moves it, and a drag picks words out of it to copy.
//
// Copying is worth doing here rather than leaving to the terminal, and the
// reason is the alternate screen: it has no scrollback, so a reply that has
// scrolled out of the window is gone unless it can be selected. Holding Shift
// still reaches the terminal's own selection for the rows that are visible, and
// that is the right answer for a screenful; this is the answer for a paragraph.

import (
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// point is one cell of the transcript: a row counted from the first row the
// transcript ever drew, and a column of that row.
//
// The row is absolute rather than a row on the screen because a selection has to
// outlive the scroll. A reader who drags four lines and then rolls the wheel has
// not changed their mind about the four lines, and a selection stored in screen
// coordinates would silently start pointing at different words.
type point struct {
	row, cell int
}

// selection is a stretch of transcript picked out with the mouse. It belongs to
// the view rather than to the program: it is about rows on a screen, and each
// conversation has its own.
type selection struct {
	anchor point
	cursor point
	// held records that the button is still down. A released selection keeps its
	// highlight — it is what the reader just copied, and unhighlighting it the
	// instant the copy lands would leave them no way to check what they got —
	// until the next press replaces it or Esc puts it away.
	held bool
}

// clear forgets the selection, which is what a new press, Esc and a fold all do.
func (s *selection) clear() {
	s.anchor, s.cursor, s.held = point{}, point{}, false
}

// empty reports that the mouse went down and came up in the same cell: a click
// rather than a drag, and a click selects nothing.
func (s selection) empty() bool { return s.anchor == s.cursor }

// bounds puts the two ends in reading order, because a drag can go up and to the
// left as easily as down and to the right.
func (s selection) bounds() (first, last point) {
	if s.anchor.row > s.cursor.row || (s.anchor.row == s.cursor.row && s.anchor.cell > s.cursor.cell) {
		return s.cursor, s.anchor
	}
	return s.anchor, s.cursor
}

// cells is the half-open cell range a selection covers on one row of a given
// width, and false when it covers nothing there.
//
// The rows in the middle of a selection are covered from end to end; the first
// and the last are covered from the cell the mouse went down on and through the
// cell it let go on, which is why those two are the only rows the ends matter
// for. The width is the row's own, not the screen's: a row that ends short ends
// where it ends, and a selection dragged past it does not select the padding.
func (s selection) cells(row, width int) (from, to int, ok bool) {
	first, last := s.bounds()
	if row < first.row || row > last.row {
		return 0, 0, false
	}
	from = 0
	if row == first.row {
		from = first.cell
	}
	to = width
	if row == last.row {
		to = last.cell + 1
	}
	if from < 0 {
		from = 0
	}
	if to > width {
		to = width
	}
	if to <= from {
		return 0, 0, false
	}
	return from, to, true
}

// The pair that paints a cell as selected. They turn one attribute on and off
// rather than resetting the row: a reset would also drop the colour the row was
// drawn in, and the selected words would be the only words on screen that had
// lost theirs.
const (
	reverseOn  = "\x1b[7m"
	reverseOff = "\x1b[27m"
)

// markSelection paints cells [from, to) of one row as selected.
//
// The attribute is toggled in place rather than the row being cut in two. A row
// carries its colours as escape sequences inside the text, and cutting it at two
// cell positions — xansi.Cut — drops the style the cut landed in, so the
// selected words would come out in the terminal's default colour. Walking the
// row as graphemes and putting the pair around the cells that belong to the
// selection leaves every other sequence exactly where it was.
func markSelection(line string, from, to int) string {
	if to <= from {
		return line
	}
	var out strings.Builder
	out.Grow(len(line) + len(reverseOn) + len(reverseOff))
	state := byte(0)
	cell := 0
	open := false
	for rest := line; rest != ""; {
		// nil for the parser: the parameters of each sequence are of no
		// interest, only how wide the grapheme it carries is.
		seq, width, n, newState := xansi.DecodeSequence(rest, state, nil)
		if n <= 0 {
			break
		}
		state = newState
		rest = rest[n:]
		if width == 0 {
			// An escape or a control byte: it paints no cell, so it cannot be
			// selected and must not be split from what follows it.
			out.WriteString(seq)
			continue
		}
		if !open && cell >= from && cell < to {
			out.WriteString(reverseOn)
			open = true
		}
		out.WriteString(seq)
		cell += width
		if open && cell >= to {
			out.WriteString(reverseOff)
			open = false
		}
	}
	if open {
		// A selection that ran past the end of the row, or a row written with a
		// wide grapheme straddling the last selected cell.
		out.WriteString(reverseOff)
	}
	return out.String()
}

// selectedText is what cells [from, to) of one row say: the styling taken off,
// and the blank cells at the end dropped, because a drag across a padded row
// should not paste its padding.
func selectedText(line string, from, to int) string {
	if to <= from {
		return ""
	}
	return strings.TrimRight(xansi.Cut(xansi.Strip(line), from, to), " \t")
}

// selectionText is what a selection says, one line per row it covers.
//
// Blank rows inside the selection are kept — a selected paragraph includes the
// empty line between its halves — and blank rows at the two ends are dropped,
// which is what a drag that overshot onto the air above or below the words would
// otherwise paste.
func selectionText(lines []string, sel selection) string {
	if sel.empty() {
		return ""
	}
	first, last := sel.bounds()
	out := make([]string, 0, last.row-first.row+1)
	for row := first.row; row <= last.row; row++ {
		if row < 0 || row >= len(lines) {
			continue
		}
		from, to, ok := sel.cells(row, xansi.StringWidth(lines[row]))
		if !ok {
			out = append(out, "")
			continue
		}
		out = append(out, selectedText(lines[row], from, to))
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

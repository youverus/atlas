package tui

// The down arrow is the way back from history. It is drawn on the last cell of
// the last body row only while the window has scrolled off the newest message,
// and a click on that cell jumps back. These tests are about that arrow: where
// it is, when it is there, and what a click on it does.

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// scrolledModel is a conversation long enough to scroll, already looking at
// history rather than at the newest message.
func scrolledModel(t *testing.T) Model {
	t.Helper()
	m := New(Config{Styles: testStyles(t), Layout: DefaultLayout()})
	m = sized(t, m)
	m.view(0).transcript = alternatingTurn()
	m.view(0).offset = 2
	return m
}

// arrowRow reads the frame's last body row: the one the arrow is drawn on.
func arrowRow(t *testing.T, m Model) string {
	t.Helper()
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) < m.bodyHeight() {
		t.Fatalf("the frame drew %d rows, want at least %d", len(lines), m.bodyHeight())
	}
	return lines[m.bodyHeight()-1]
}

// TestTheDownArrowAppearsOnlyOffTheNewestMessage: the arrow is an offer to
// undo the scroll, and at the newest message there is nothing to undo.
func TestTheDownArrowAppearsOnlyOffTheNewestMessage(t *testing.T) {
	m := scrolledModel(t)
	if !strings.HasSuffix(plain(arrowRow(t, m)), "↓") {
		t.Fatalf("the scrolled frame has no arrow on its last body row: %q", plain(arrowRow(t, m)))
	}

	m.view(0).offset = 0
	if strings.Contains(plain(arrowRow(t, m)), "↓") {
		t.Fatalf("the arrow is up at the newest message: %q", plain(arrowRow(t, m)))
	}
}

// TestAClickOnTheDownArrowJumpsToTheNewest: the click lands on the one cell
// the arrow occupies, and the window is back at the newest message with the
// selection put away.
func TestAClickOnTheDownArrowJumpsToTheNewest(t *testing.T) {
	m := scrolledModel(t)
	m.view(0).sel = selection{anchor: point{row: 1, cell: 0}, cursor: point{row: 1, cell: 4}}

	m = pressAt(t, m, m.bodyHeight()-1, m.width-1)
	if m.view(0).offset != 0 {
		t.Fatalf("the click left the window %d rows from the newest", m.view(0).offset)
	}
	if !m.view(0).sel.empty() {
		t.Fatal("the jump kept a selection made in the history it left")
	}
	if strings.Contains(plain(arrowRow(t, m)), "↓") {
		t.Fatal("the arrow is still up at the newest message")
	}
}

// TestAClickNextToTheDownArrowDoesNotJump: only the arrow's own cell is the
// way back; the row around it is still transcript to select.
func TestAClickNextToTheDownArrowDoesNotJump(t *testing.T) {
	m := scrolledModel(t)
	m = pressAt(t, m, m.bodyHeight()-1, m.width-2)
	if m.view(0).offset != 2 {
		t.Fatalf("a click beside the arrow moved the window to offset %d", m.view(0).offset)
	}
}

// TestDownArrowLineDropsAStraddlingGlyph: a wide grapheme covering the arrow's
// cell is dropped whole rather than cut, and the row keeps its width.
func TestDownArrowLineDropsAStraddlingGlyph(t *testing.T) {
	m := scrolledModel(t)
	line := strings.Repeat("a", m.width-2) + "你"
	if got := xansi.StringWidth(line); got != m.width {
		t.Fatalf("the fixture is %d cells wide, want %d", got, m.width)
	}

	got := m.downArrowLine(line)
	if strings.Contains(plain(got), "你") {
		t.Fatalf("the straddling glyph survived: %q", plain(got))
	}
	if w := xansi.StringWidth(got); w != m.width {
		t.Fatalf("the arrow row is %d cells wide, want %d: %q", w, m.width, plain(got))
	}
	if !strings.HasSuffix(plain(got), "↓") {
		t.Fatalf("the arrow is not the last cell: %q", plain(got))
	}
}

// TestDownArrowLineFillsAnEmptyRow: the last row of the body can be blank
// history, and the arrow still lands on its last cell.
func TestDownArrowLineFillsAnEmptyRow(t *testing.T) {
	m := scrolledModel(t)
	got := m.downArrowLine("")
	if w := xansi.StringWidth(got); w != m.width {
		t.Fatalf("the arrow row is %d cells wide, want %d", w, m.width)
	}
	if !strings.HasSuffix(plain(got), "↓") {
		t.Fatalf("the arrow is not the last cell: %q", plain(got))
	}
}

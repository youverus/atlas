package tui

// The up arrow is the way back to the very beginning. It is drawn on the last
// cell of the first body row while older conversation is held back or the
// window has scrolled off the first row, and a click on that cell releases
// everything and jumps to it. These tests are about that arrow: where it is,
// when it is there, and what a click on it does.

import (
	"strings"
	"testing"
)

// topRow reads the frame's first body row: the one the up arrow is drawn on.
func topRow(t *testing.T, m Model) string {
	t.Helper()
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) < 1 {
		t.Fatalf("the frame drew no rows")
	}
	return lines[0]
}

// heldBack is a resumed session whose history is still held back: the window
// shows only the newest answer.
func heldBack(t *testing.T) Model {
	t.Helper()
	return replayed(t, 40)
}

// TestTheUpArrowAppearsWhileHistoryIsHeldBack: a resumed session enters with
// everything but the newest answer held back, and the arrow offers the whole
// conversation.
func TestTheUpArrowAppearsWhileHistoryIsHeldBack(t *testing.T) {
	m := heldBack(t)
	if m.views[ViewMain].transcript.Hidden() == 0 {
		t.Fatal("the fixture holds nothing back")
	}
	if !strings.HasSuffix(plain(topRow(t, m)), "↑") {
		t.Fatalf("the frame has no up arrow on its first body row: %q", plain(topRow(t, m)))
	}
}

// TestTheUpArrowAppearsWhenScrolledOffTheFirstRow: even with nothing held
// back, a window that has scrolled down offers the way back up.
func TestTheUpArrowAppearsWhenScrolledOffTheFirstRow(t *testing.T) {
	m := scrolledModel(t)
	if m.views[ViewMain].transcript.Hidden() != 0 {
		t.Fatal("the fixture holds history back")
	}
	if !strings.HasSuffix(plain(topRow(t, m)), "↑") {
		t.Fatalf("the scrolled frame has no up arrow on its first body row: %q", plain(topRow(t, m)))
	}
}

// TestTheUpArrowIsAbsentAtTheNewestMessage: at the newest message with nothing
// held back there is nowhere further up to go.
func TestTheUpArrowIsAbsentAtTheNewestMessage(t *testing.T) {
	m := scrolledModel(t)
	m.view(0).offset = 0
	if strings.Contains(plain(topRow(t, m)), "↑") {
		t.Fatalf("the arrow is up at the newest message: %q", plain(topRow(t, m)))
	}
}

// TestAClickOnTheUpArrowJumpsToTheOldest: the click lands on the one cell the
// arrow occupies, the held-back history is released whole, and the window is
// at the first row with the selection put away.
func TestAClickOnTheUpArrowJumpsToTheOldest(t *testing.T) {
	m := heldBack(t)
	m.view(0).sel = selection{anchor: point{row: 1, cell: 0}, cursor: point{row: 1, cell: 4}}

	m = pressAt(t, m, 0, m.width-1)
	if hidden := m.views[ViewMain].transcript.Hidden(); hidden != 0 {
		t.Fatalf("the click left %d spans held back", hidden)
	}
	if offset := m.view(0).offset; offset != m.maxOffset() {
		t.Fatalf("the click left the window %d rows from the first, want %d", offset, m.maxOffset())
	}
	if !m.view(0).sel.empty() {
		t.Fatal("the jump kept a selection made in the history it left")
	}
	if top := plain(m.bodyLines()[0]); !strings.Contains(top, "answer 0") {
		t.Fatalf("top of the conversation = %q, want the first answer", top)
	}
}

// TestAClickOnTheUpArrowReachesTheFirstRowWithoutHeldBackHistory: the jump is
// a scroll too, not only a release — a window with nothing held back still
// scrolls to the very first row.
func TestAClickOnTheUpArrowReachesTheFirstRowWithoutHeldBackHistory(t *testing.T) {
	m := scrolledModel(t)
	m = pressAt(t, m, 0, m.width-1)
	if offset := m.view(0).offset; offset != m.maxOffset() {
		t.Fatalf("the click left the window %d rows from the first, want %d", offset, m.maxOffset())
	}
	if top := plain(m.bodyLines()[0]); !strings.Contains(top, "why is the suite slow") {
		t.Fatalf("top of the conversation = %q, want the first prompt", top)
	}
}

// TestAClickNextToTheUpArrowDoesNotJump: only the arrow's own cell is the way
// back; the row around it is still transcript to select.
func TestAClickNextToTheUpArrowDoesNotJump(t *testing.T) {
	m := heldBack(t)
	m = pressAt(t, m, 0, m.width-2)
	if hidden := m.views[ViewMain].transcript.Hidden(); hidden != 39 {
		t.Fatalf("a click beside the arrow revealed history: hidden = %d", hidden)
	}
	if m.view(0).offset != 0 {
		t.Fatalf("a click beside the arrow moved the window to offset %d", m.view(0).offset)
	}
}

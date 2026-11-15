package tui

// The mouse has one gesture with two meanings, and the difference is only
// visible once the button is back up: a press that stayed put folds the stretch
// it landed on, and a press that moved copies what it crossed. These tests are
// about that decision, and about the receipt it leaves in the status bar.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// mouseModel is a conversation with both kinds of stretch in it, sized so the
// whole turn is on screen.
func mouseModel(t *testing.T) Model {
	t.Helper()
	m := New(Config{Styles: testStyles(t), Layout: DefaultLayout()})
	m = sized(t, m)
	m.view(0).transcript = alternatingTurn()
	return m
}

// gesture feeds one mouse message and hands back the command the model produced
// for it, which is where the copy lives.
func gesture(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	return m.handle(msg)
}

func pressAt(t *testing.T, m Model, y, x int) Model {
	t.Helper()
	next, _ := gesture(t, m, tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	return next
}

func dragTo(t *testing.T, m Model, y, x int) Model {
	t.Helper()
	next, _ := gesture(t, m, tea.MouseMotionMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	return next
}

func releaseAt(t *testing.T, m Model, y, x int) (Model, tea.Cmd) {
	t.Helper()
	return gesture(t, m, tea.MouseReleaseMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
}

// screenRow is where a row of the transcript is drawn, using the same window
// arithmetic the frame and the mouse share.
func screenRow(t *testing.T, m *Model, row int) int {
	t.Helper()
	lines := m.transcriptLines(m.active, m.width)
	start, end, pad := m.bodyWindow(lines)
	if row < start || row >= end {
		t.Fatalf("transcript row %d is outside the window [%d,%d)", row, start, end)
	}
	return pad + row - start
}

// rowOfSpan is the transcript row the title of one stretch is drawn on.
func rowOfSpan(t *testing.T, m *Model, index int) int {
	t.Helper()
	frame := m.view(0).transcript.Frame(m.width, m.cfg.Styles, m.cfg.Layout)
	rows, ok := frame.RowsFor(index)
	if !ok {
		t.Fatalf("stretch %d drew no rows", index)
	}
	return rows.First
}

// TestAClickOnATitleFoldsTheStretch: the gesture is decided on the release, so
// a press and a release in the same cell is a fold and never a copy.
func TestAClickOnATitleFoldsTheStretch(t *testing.T) {
	m := mouseModel(t)
	row := rowOfSpan(t, &m, 1)
	y := screenRow(t, &m, row)

	m = pressAt(t, m, y, 0)
	m, cmd := releaseAt(t, m, y, 0)
	if cmd != nil {
		t.Fatal("a fold asked for something to be copied")
	}
	if !m.view(0).transcript.Spans()[1].Collapsed {
		t.Fatalf("the stretch titled %q did not fold", plain(m.transcriptLines(0, m.width)[row]))
	}
	if !m.view(0).sel.empty() {
		t.Fatal("the click left a selection behind")
	}
}

// TestAClickOnABodyIsNotAFold: the handle of a box is its title, and a press
// inside the box is the reader after the words, not after the shape.
func TestAClickOnABodyIsNotAFold(t *testing.T) {
	m := mouseModel(t)
	row := rowOfSpan(t, &m, 1) + 1
	y := screenRow(t, &m, row)

	m = pressAt(t, m, y, 0)
	m, _ = releaseAt(t, m, y, 0)
	if m.view(0).transcript.Spans()[1].Collapsed {
		t.Fatal("a click inside a box folded it")
	}
	if !m.view(0).sel.empty() {
		t.Fatal("a click on an unselectable cell kept a selection")
	}
}

// TestADragCopiesTheWordsItCrossed: the receipt is the only thing that says the
// copy happened, and it counts characters because that is what the reader can
// compare against what they meant to take.
func TestADragCopiesTheWordsItCrossed(t *testing.T) {
	m := mouseModel(t)
	lines := m.transcriptLines(0, m.width)
	row, width := -1, 0
	for index, line := range lines {
		if strings.Contains(plain(line), "The gate runs it once") {
			row, width = index, len([]rune(plain(line)))
			break
		}
	}
	if row < 0 {
		t.Fatalf("the answer is not on screen:\n%s", strings.Join(lines, "\n"))
	}
	y := screenRow(t, &m, row)

	m = pressAt(t, m, y, 0)
	m = dragTo(t, m, y, width-1)
	m, cmd := releaseAt(t, m, y, width-1)
	if cmd == nil {
		t.Fatal("the copy was never asked for")
	}
	if !strings.HasPrefix(m.copied, "copied ") || m.copied == copiedField("") {
		t.Fatalf("the receipt says %q", m.copied)
	}
	if !strings.Contains(m.statusLine(), m.copied) {
		t.Fatalf("the receipt %q is not in the status bar %q", m.copied, plain(m.statusLine()))
	}
	if m.view(0).sel.empty() {
		t.Fatal("the copy dropped the highlight it was made from")
	}
}

// TestOneCharacterIsOneCharacter: the receipt says "char" rather than "chars"
// for a drag that caught one, because a line that says "copied 1 chars" reads
// like a bug in the count.
func TestOneCharacterIsOneCharacter(t *testing.T) {
	if got := copiedField("x"); got != "copied 1 char" {
		t.Fatalf("one character reads %q", got)
	}
	if got := copiedField("xy"); got != "copied 2 chars" {
		t.Fatalf("two characters read %q", got)
	}
}

// TestTheNextKeyEndsTheReceipt: the receipt is about the gesture before last,
// and a key is the reader moving on from it.
func TestTheNextKeyEndsTheReceipt(t *testing.T) {
	m := mouseModel(t)
	m.copied = copiedField("abc")
	next, _ := m.handleKey(typeText("h"))
	if next.copied != "" {
		t.Fatalf("the receipt %q survived a key", next.copied)
	}
}

// TestEscPutsTheSelectionAway: a highlight is a claim about what a copy would
// take, and Esc is the reader taking it back.
func TestEscPutsTheSelectionAway(t *testing.T) {
	m := mouseModel(t)
	m.view(0).sel = selection{anchor: point{row: 1, cell: 0}, cursor: point{row: 1, cell: 4}}
	m.copied = copiedField("abcd")

	next, _ := m.handleKey(press(tea.KeyEscape, 0))
	if !next.view(0).sel.empty() {
		t.Fatal("Esc left the selection up")
	}
	if next.copied != "" {
		t.Fatalf("Esc left the receipt %q", next.copied)
	}
}

// TestCtrlGFoldsEveryStretch: the key answers "show me the shape of the turn",
// so one press closes everything and the next opens everything.
func TestCtrlGFoldsEveryStretch(t *testing.T) {
	m := mouseModel(t)
	next, _ := m.handleKey(press('g', tea.ModCtrl))
	folded := 0
	for _, span := range next.view(0).transcript.Spans() {
		if span.Collapsed {
			folded++
		}
	}
	if folded == 0 {
		t.Fatal("Ctrl+G folded nothing")
	}
	again, _ := next.handleKey(press('g', tea.ModCtrl))
	for _, span := range again.view(0).transcript.Spans() {
		if span.Collapsed {
			t.Fatalf("the second Ctrl+G left %v folded", span)
		}
	}
}

// TestCtrlGDoesNothingWithoutBoxes: folded rows exist only where there is a box
// to fold into, and a key that folded nothing visible would be a key that
// appears broken.
func TestCtrlGDoesNothingWithoutBoxes(t *testing.T) {
	m := mouseModel(t)
	layout := DefaultLayout()
	layout.TranscriptFrames = false
	m.cfg.Layout = layout

	next, _ := m.handleKey(press('g', tea.ModCtrl))
	for _, span := range next.view(0).transcript.Spans() {
		if span.Collapsed {
			t.Fatalf("Ctrl+G folded %v with the boxes off", span)
		}
	}
}

// TestAPressOnTheHomePageSelectsNothing: there is no conversation on that
// screen, so a press there must not start a selection that would copy the
// welcome text as if it were history.
func TestAPressOnTheHomePageSelectsNothing(t *testing.T) {
	m := New(Config{Styles: testStyles(t), Layout: DefaultLayout()})
	m = sized(t, m)

	m = pressAt(t, m, 0, 0)
	if !m.view(0).sel.empty() {
		t.Fatal("a press on the home page started a selection")
	}
	m, _ = releaseAt(t, m, 0, 0)
	if !m.view(0).sel.empty() {
		t.Fatal("a release on the home page left a selection")
	}
}

// TestADragPastTheTopOfTheWindowStopsAtItsFirstRow: the window is a hole in a
// longer conversation, and a pointer above it means "up there" rather than
// nothing.
func TestADragPastTheTopOfTheWindowStopsAtItsFirstRow(t *testing.T) {
	m := mouseModel(t)
	lines := m.transcriptLines(0, m.width)
	start, end, _ := m.bodyWindow(lines)
	if end-start < 2 {
		t.Skip("the turn is too short for this to mean anything")
	}
	last := screenRow(t, &m, end-1)

	m = pressAt(t, m, last, 0)
	m = dragTo(t, m, 0, 4)
	if got := m.view(0).sel.cursor.row; got != start {
		t.Fatalf("a drag above the window named row %d, want the first on screen %d", got, start)
	}
}

// TestADragPastTheBottomOfTheWindowStopsAtItsLastRow is the other end of the
// same rule.
func TestADragPastTheBottomOfTheWindowStopsAtItsLastRow(t *testing.T) {
	m := mouseModel(t)
	lines := m.transcriptLines(0, m.width)
	start, end, _ := m.bodyWindow(lines)
	if end-start < 2 {
		t.Skip("the turn is too short for this to mean anything")
	}
	first := screenRow(t, &m, start)

	m = pressAt(t, m, first, 0)
	m = dragTo(t, m, m.height, 4)
	if got := m.view(0).sel.cursor.row; got != end-1 {
		t.Fatalf("a drag below the window named row %d, want the last on screen %d", got, end-1)
	}
}

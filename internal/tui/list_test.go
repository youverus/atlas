package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func numbers(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// TestListWindowKeepsThePickedRowInView is the rule both lists depend on: the
// row holding the keys is always one of the rows on screen, and the window
// slides under it rather than letting the highlight leave.
func TestListWindowKeepsThePickedRowInView(t *testing.T) {
	items := numbers(10)
	for _, chosen := range []int{0, 1, 2, 3, 4, 7, 9} {
		for _, rows := range []int{1, 2, 3, 5, 10, 40} {
			visible, row, first := listWindow(items, chosen, rows)
			if row < 0 || row >= len(visible) {
				t.Fatalf("chosen=%d rows=%d: row %d is outside %d visible", chosen, rows, row, len(visible))
			}
			if got := visible[row]; got != chosen {
				t.Fatalf("chosen=%d rows=%d: row %d shows %d", chosen, rows, row, got)
			}
			if first+row != chosen {
				t.Fatalf("chosen=%d rows=%d: first=%d row=%d", chosen, rows, first, row)
			}
			if want := min(rows, len(items)); len(visible) != want {
				t.Fatalf("chosen=%d rows=%d: %d visible, want %d", chosen, rows, len(visible), want)
			}
		}
	}
}

// TestListWindowPutsThePickedRowAtTheBottom pins where the window lands, which
// the loop above cannot see: up to the last page the window stays put at the
// top, and from then on the picked row sits on the last visible row. A window
// that scrolled ahead of the highlight would keep the picked row "in view" in
// the weakest sense while the reader watched the mark climb back to the top
// after every keypress.
func TestListWindowPutsThePickedRowAtTheBottom(t *testing.T) {
	items := numbers(10)
	visible, row, first := listWindow(items, 4, 3)
	if first != 2 || row != 2 || visible[row] != 4 {
		t.Fatalf("first=%d row=%d, want 2 and 2", first, row)
	}
	if _, row, first = listWindow(items, 3, 3); first != 1 || row != 2 {
		t.Fatalf("first=%d row=%d, want 1 and 2", first, row)
	}
	if _, row, first = listWindow(items, 2, 3); first != 0 || row != 2 {
		t.Fatalf("first=%d row=%d, want 0 and 2", first, row)
	}
	if _, row, first = listWindow(items, 1, 3); first != 0 || row != 1 {
		t.Fatalf("first=%d row=%d, want 0 and 1", first, row)
	}
}

func TestListWindowWhenEverythingFits(t *testing.T) {
	items := numbers(3)
	visible, row, first := listWindow(items, 2, 5)
	if first != 0 || row != 2 || len(visible) != 3 {
		t.Fatalf("visible=%v row=%d first=%d", visible, row, first)
	}
}

func TestListWindowOnAnEmptyList(t *testing.T) {
	visible, row, first := listWindow([]int(nil), 4, 5)
	if visible != nil || row != 0 || first != 0 {
		t.Fatalf("visible=%v row=%d first=%d", visible, row, first)
	}
}

// TestListWindowFloorsAtOneRow is the guard that keeps a caller's wrong number
// from becoming a panic: the slice arithmetic below it needs at least one row.
func TestListWindowFloorsAtOneRow(t *testing.T) {
	items := numbers(4)
	for _, rows := range []int{0, -1, -10} {
		visible, row, first := listWindow(items, 2, rows)
		if len(visible) != 1 || visible[row] != 2 || first != 2 {
			t.Fatalf("rows=%d: visible=%v row=%d first=%d", rows, visible, row, first)
		}
	}
}

// TestListWindowClampsAStaleIndex covers the caller whose list changed under it:
// an index past the end lands on the last row instead of on nothing.
func TestListWindowClampsAStaleIndex(t *testing.T) {
	visible, row, _ := listWindow(numbers(4), 99, 3)
	if visible[row] != 3 {
		t.Fatalf("visible=%v row=%d", visible, row)
	}
	if _, row, _ = listWindow(numbers(4), -7, 3); row != 0 {
		t.Fatalf("row=%d, want 0", row)
	}
}

// TestLabelColumnMeasuresTheVisibleRows is why the column belongs to the window
// rather than to the list: a column that grew as the window slid would move
// every row sideways under the reader's eyes.
func TestLabelColumnMeasuresTheVisibleRows(t *testing.T) {
	items := []string{"a", "bbb", "cc"}
	if got := labelColumn(items, func(s string) string { return s }); got != 3 {
		t.Fatalf("labelColumn = %d, want 3", got)
	}
	if got := labelColumn(items[1:], func(s string) string { return s }); got != 3 {
		t.Fatalf("labelColumn = %d, want 3", got)
	}
	if got := labelColumn(items[:2], func(s string) string { return s }); got != 3 {
		t.Fatalf("labelColumn = %d, want 3", got)
	}
	// Cells, not runes: a menu of model names is free to be set in any script.
	if got := labelColumn([]string{"宽字符"}, func(s string) string { return s }); got != 6 {
		t.Fatalf("labelColumn = %d, want 6", got)
	}
	if got := labelColumn([]string(nil), func(s string) string { return s }); got != 0 {
		t.Fatalf("labelColumn = %d, want 0", got)
	}
}

func TestNoteColumnPaysForTheMarkerAndTheGap(t *testing.T) {
	for _, total := range []int{0, 20, 80} {
		for _, label := range []int{0, 5, 30} {
			want := total - listMarker - label - listColumnGap
			if got := noteColumn(total, label); got != want {
				t.Fatalf("noteColumn(%d, %d) = %d, want %d", total, label, got, want)
			}
		}
	}
}

// TestListRowSpellsTheSelectedRow is the one row the two lists must agree on: a
// marker in front, the label in its column, the note after the gap.
func TestListRowSpellsTheSelectedRow(t *testing.T) {
	s := testStyles(t)

	got := plain(listRow(true, "model", "openai", 8, 20, s.Text, s))
	want := "▸ model     openai"
	if got != want {
		t.Fatalf("selected = %q, want %q", got, want)
	}

	got = plain(listRow(false, "model", "openai", 8, 20, s.Text, s))
	want = "  model     openai"
	if got != want {
		t.Fatalf("idle = %q, want %q", got, want)
	}
}

// TestListRowLeavesNoHalfAColumn covers the two ways the note goes away: there
// is none, and there is no room worth giving it.
func TestListRowLeavesNoHalfAColumn(t *testing.T) {
	s := testStyles(t)
	if got := plain(listRow(true, "model", "", 8, 20, s.Text, s)); got != "▸ model   " {
		t.Fatalf("empty note = %q", got)
	}
	narrow := listRow(true, "model", "openai", 8, minAboutWidth-1, s.Text, s)
	if got := plain(narrow); got != "▸ model   " {
		t.Fatalf("narrow column = %q", got)
	}
	if got, want := plain(listRow(true, "model", "openai", 8, minAboutWidth, s.Text, s)), "▸ model     openai"; got != want {
		t.Fatalf("at the threshold = %q, want %q", got, want)
	}
}

// TestListRowCutsTheLabelToItsColumn guards the one place a caller's width is
// obeyed even when the text is longer: a box narrows the label column when the
// axis is up, and the row must not overflow it.
func TestListRowCutsTheLabelToItsColumn(t *testing.T) {
	s := testStyles(t)
	line := plain(listRow(true, "abcdefghijklmnop", "", 6, 0, s.Text, s))
	if line != "▸ abcdef" {
		t.Fatalf("line = %q", line)
	}
	if width := lipgloss.Width(line); width != listMarker+6 {
		t.Fatalf("width = %d, want %d", width, listMarker+6)
	}
}

// TestListRowHighlightComesFromOnePlace is the anti-drift assertion: the picked
// row is the accent one in both lists, and the axis column borrows the same
// rule instead of deciding for itself.
func TestListRowHighlightComesFromOnePlace(t *testing.T) {
	s := resolvedStyles(t)
	selected := listRow(true, "model", "", 8, 0, s.Dim, s)
	if !strings.Contains(selected, s.Accent.Bold(true).Render("▸ ")) {
		t.Fatalf("selected row is not drawn in the accent style: %q", selected)
	}
	idle := listRow(false, "model", "", 8, 0, s.Dim, s)
	if !strings.Contains(idle, s.Dim.Render("  ")) {
		t.Fatalf("idle row is not dim: %q", idle)
	}
	if got := rowLabelStyle(true, s.Dim, s); got.Render("x") != s.Accent.Bold(true).Render("x") {
		t.Fatalf("rowLabelStyle(true) is not the accent style")
	}
	if got := rowLabelStyle(false, s.Dim, s); got.Render("x") != s.Dim.Render("x") {
		t.Fatalf("rowLabelStyle(false) is not the idle style")
	}
}

// The rest of this file is the other half of the contract: the two lists must
// actually be drawn by the kernel. Each assertion is written against a shape the
// shared row cannot produce on its own — a column that was not reserved, a style
// the caller passed in the wrong slot — so it fails if either list grows a
// second opinion about its rows.

// TestPaletteRowIsTheSharedRow pins the palette's column arithmetic: the row is
// exactly the screen wide, which is what tells the note column from the gutter.
func TestPaletteRowIsTheSharedRow(t *testing.T) {
	s := testStyles(t)
	long := strings.Repeat("x", 200)
	lines := suggestionLines([]Suggestion{{Name: "/model", About: long}}, 0, 80, s, DefaultLayout())
	if len(lines) != 1 {
		t.Fatalf("palette has %d rows, want 1", len(lines))
	}
	want := listRow(true, "/model", long, 6, noteColumn(80, 6), s.Dim, s)
	if lines[0] != want {
		t.Fatalf("row = %q, want %q", lines[0], want)
	}
	if got := lipgloss.Width(lines[0]); got != 80 {
		t.Fatalf("row is %d cells wide, want the screen's 80", got)
	}
}

// TestPaletteKeepsItsIdleRowsDim: a hint is not a name, and the picked row is
// the only one drawn as one.
func TestPaletteKeepsItsIdleRowsDim(t *testing.T) {
	s := resolvedStyles(t)
	picks := []Suggestion{{Name: "/model", About: "switch model"}, {Name: "/help", About: "keys"}}
	lines := suggestionLines(picks, 0, 80, s, DefaultLayout())
	if !strings.Contains(lines[0], s.Accent.Bold(true).Render("▸ ")) {
		t.Fatalf("the picked row is not the accent one: %q", lines[0])
	}
	if want := s.Dim.Render(padRight("/help", 6)); !strings.Contains(lines[1], want) {
		t.Fatalf("the idle row is not dim: %q", lines[1])
	}
}

// TestMenuLinesAreAllTheSameWidth is the invariant a frame has: the box is a
// rectangle. It fails the moment a column is over-reserved, which is what the
// axis costs beside the note.
func TestMenuLinesAreAllTheSameWidth(t *testing.T) {
	s := testStyles(t)
	items := []Choice{
		{Value: "a", Label: "alpha", Note: "openai-completions", Steps: []string{"off", "high"}, Step: 1},
		{Value: "b", Label: "beta", Note: "openai-responses and then some", Steps: []string{"off", "medium", "high"}},
	}
	msg := &PickMsg{Request: PickRequest{Title: "model", Items: items, Axis: "thinking"}, Reply: make(chan PickAnswer, 1)}
	for _, width := range []int{34, 35, 60, 72, 100} {
		box := pickBox(msg, items, 0, "", 12, width, s, DefaultLayout())
		want := lipgloss.Width(box[0])
		for i, line := range box {
			if got := lipgloss.Width(line); got != want {
				t.Fatalf("width %d: line %d is %d cells, the frame is %d: %q", width, i, got, want, plain(line))
			}
		}
		if want != width {
			t.Fatalf("width %d: the box is %d cells wide", width, want)
		}
	}
}

// TestMenuDropsTheAxisBeforeItSqueezesTheNote is the 0.4.4 rule with its
// boundary written out: one cell narrower and the column the reader turns is the
// one that goes. The numbers are literal because a test that recomputed them
// from the formula would agree with any formula.
func TestMenuDropsTheAxisBeforeItSqueezesTheNote(t *testing.T) {
	s := testStyles(t)
	items := []Choice{
		{Value: "a", Label: "alpha", Note: "openai", Steps: []string{"off", "low", "medium", "high", "max"}},
		{Value: "b", Label: "bravo", Note: "openai", Steps: []string{"off", "low", "medium", "high", "max"}},
	}
	msg := &PickMsg{Request: PickRequest{Title: "model", Items: items, Axis: "thinking"}, Reply: make(chan PickAnswer, 1)}
	footer := func(width int) string {
		box := pickBox(msg, items, 0, "", 12, width, s, DefaultLayout())
		return plain(box[len(box)-2])
	}
	if got := footer(35); !strings.Contains(got, "←→ thinking") {
		t.Fatalf("at width 35 the axis is gone: %q", got)
	}
	if got := footer(34); strings.Contains(got, "←→") {
		t.Fatalf("at width 34 the axis is still advertised: %q", got)
	}
}

// TestMenuDrawsTheIdleAxisDim: the guillemets are the only mark of where the
// arrows act, so the rows that do not hold the keys must not wear them.
func TestMenuDrawsTheIdleAxisDim(t *testing.T) {
	s := resolvedStyles(t)
	items := []Choice{
		{Value: "a", Label: "alpha", Note: "openai", Steps: []string{"off", "high"}, Step: 1},
		{Value: "b", Label: "bravo", Note: "openai", Steps: []string{"off", "high"}, Step: 0},
	}
	msg := &PickMsg{Request: PickRequest{Title: "model", Items: items, Axis: "thinking"}, Reply: make(chan PickAnswer, 1)}
	box := pickBox(msg, items, 0, "", 12, 60, s, DefaultLayout())
	s = s.Panel() // the box rebuilds its styles on the raised surface

	// The picked row is first: its axis word is the accent one, in guillemets.
	axis := axisWidth(items)
	if want := s.Accent.Bold(true).Render(padLeft("‹ high ›", axis)); !strings.Contains(box[2], want) {
		t.Fatalf("the picked row's axis is not the accent one: %q", box[2])
	}
	if want := s.Dim.Render(padLeft("off", axis)); !strings.Contains(box[3], want) {
		t.Fatalf("the idle row's axis is not dim: %q", box[3])
	}
	if strings.Contains(box[3], "‹") {
		t.Fatalf("the idle row wears the picked row's marks: %q", box[3])
	}
}

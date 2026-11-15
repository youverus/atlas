package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// This file is the row layout the two lists share. A reader makes one gesture —
// a marker on the left, a name in a column, a note beside it, arrows that slide
// the window — and atlas draws it twice: the modal menu a command opens
// (pick.go) and the palette above the composer (suggest.go). They disagreed
// about two things, and both are passed in here: what a row whose turn it is
// not looks like (a menu row is readable, a palette row is a hint) and whether
// the note is padded to its column (the menu's axis column follows it).
// Everything else — the marker glyph, the highlight, the air between the
// columns, where the window lands, how wide the second column gets — is decided
// once, because two lists that drift make the same key do slightly different
// things.
//
// What is deliberately not here is the state machine around the rows: the menu
// owns every key while it is up and hides the caret, while the palette lives
// above the line being typed and lets Tab mean "complete". Sharing that would
// turn "what does Esc close" into a condition.

const (
	// listMarker is the width of "▸ " and of the spaces that stand in for it,
	// so labels line up whether or not the row is the one taking the keys.
	listMarker = 2

	// listColumnGap is the air between the label column and the note.
	listColumnGap = 2

	// minAboutWidth is where the note stops being worth its column: below it
	// the line is a fragment, and a fragment beside a name reads as part of the
	// name.
	minAboutWidth = 12
)

// listWindow trims a list to the rows that fit and slides the window so the
// picked row stays visible: pressing Down at the bottom should move the window,
// not the highlight off the screen. first is the index the window starts at,
// which is what lets the caller say "8–12 of 40".
func listWindow[T any](items []T, chosen, rows int) (visible []T, row, first int) {
	if len(items) == 0 {
		return nil, 0, 0
	}
	if rows < 1 {
		// A window with no row in it cannot be answered, and both callers keep
		// at least one; the floor is here so that a number they got wrong
		// cannot turn the slice arithmetic below into a panic.
		rows = 1
	}
	chosen = clampIndex(chosen, len(items))
	if len(items) <= rows {
		return items, chosen, 0
	}
	start := chosen - rows + 1
	if start < 0 {
		start = 0
	}
	if start > len(items)-rows {
		start = len(items) - rows
	}
	return items[start : start+rows], chosen - start, start
}

// labelColumn is the widest label among the rows that are showing, which is
// what the left column is padded to. It is measured on the visible rows rather
// than on the whole list: a window that grew a column as it slid would move
// every row sideways under the reader's eyes.
func labelColumn[T any](items []T, label func(T) string) int {
	widest := 0
	for _, item := range items {
		if cells := lipgloss.Width(label(item)); cells > widest {
			widest = cells
		}
	}
	return widest
}

// noteColumn is what the second column gets out of total once the marker, the
// label and the air between them are paid for.
func noteColumn(total, label int) int {
	return total - listMarker - label - listColumnGap
}

// rowLabelStyle is what a row's label is drawn with: the row taking the keys is
// the accent one in both lists, which is why the axis column can borrow this
// instead of deciding the highlight for itself.
func rowLabelStyle(selected bool, idle lipgloss.Style, s Styles) lipgloss.Style {
	if selected {
		return s.Accent.Bold(true)
	}
	return idle
}

// listRow lays out one row: the marker, the label in its column, and the note
// beside it when the column is wide enough to say anything. idle is the label's
// style for the rows whose turn it is not — the one thing the two lists spell
// differently, so that the picked row looks the same wherever it appears.
//
// The note arrives already padded when its column has to stay lined up with
// something to its right; that rule belongs to the caller that owns that
// column.
func listRow(selected bool, label, note string, labelWidth, noteWidth int, idle lipgloss.Style, s Styles) string {
	marker, noteStyle := "  ", s.Dim
	if selected {
		marker, noteStyle = "▸ ", s.Text
	}
	labelStyle := rowLabelStyle(selected, idle, s)
	line := labelStyle.Render(marker) + labelStyle.Render(padRight(cutCells(label, labelWidth), labelWidth))
	if noteWidth >= minAboutWidth && note != "" {
		// The gap is painted with the row's own background: the palette draws
		// it on the screen, the menu on the raised surface, and a bare space
		// would show whatever the terminal had there instead of either.
		line += s.Background.Render(strings.Repeat(" ", listColumnGap)) + noteStyle.Render(cutCells(note, noteWidth))
	}
	return line
}

package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Suggestion is one command the line could become: the name as it is typed, the
// argument it takes, and the one line that says what it does.
//
// The host owns the table. The interface has never known a command name, and a
// copy of the list here would be a second answer to "what exists" — the kind
// that goes wrong the first time a command is added.
type Suggestion struct {
	Name  string // "/model", or "@internal/tui/model.go"
	Usage string // "<name>[:level]", empty when the command takes no argument
	About string // "switch model, or pick one from the list"
	// Replace is how many runes at the end of the line this suggestion stands
	// for. Zero means the whole line: a command is the line, and filling one in
	// replaces what was typed. A file reference is one token of a sentence —
	// "look at @mai" — so it carries the length of the token it would replace
	// and leaves the words around it alone.
	Replace int
}

// Label is the name and its argument, the left column of the list.
func (s Suggestion) Label() string {
	if s.Usage == "" {
		return s.Name
	}
	return s.Name + " " + s.Usage
}

// suggestionLines draws the command palette above the composer. row is the entry
// Enter and Tab would take; width is the whole screen, because the description
// sits in the second column and is measured against the right edge. How many
// rows the palette may take is the layout's PaletteRows, and zero is a palette
// that is off.
func suggestionLines(picks []Suggestion, row, width int, s Styles, l Layout) []string {
	if len(picks) == 0 || l.PaletteRows < 1 || width < 2*listMarker {
		return nil
	}
	visible, row, _ := listWindow(picks, row, l.PaletteRows)
	label := labelColumn(visible, func(pick Suggestion) string { return pick.Label() })
	// Only the note's own column is reserved beyond the label: a long
	// description is cut rather than wrapped, because a list that changed
	// height as it narrowed would push the conversation around.
	about := noteColumn(width, label)

	out := make([]string, 0, len(visible))
	for i, pick := range visible {
		out = append(out, listRow(i == row, pick.Label(), pick.About, label, about, s.Dim, s))
	}
	return out
}

// padRight fills a label to the column width. Labels are ASCII command names, so
// counting runes and counting cells agree here — but the arithmetic still goes
// through lipgloss for the argument hints a host is free to spell in any script.
func padRight(text string, width int) string {
	if gap := width - lipgloss.Width(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// padLeft fills in front of text, which is how the menu's axis column keeps its
// words against the right edge whether or not the row is the one holding the
// keys.
func padLeft(text string, width int) string {
	if gap := width - lipgloss.Width(text); gap > 0 {
		return strings.Repeat(" ", gap) + text
	}
	return text
}

// cutCells cuts text to width cells, dropping the whole tail rather than
// wrapping: the one place this is used is a single row of a list.
func cutCells(text string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	return strings.Split(lipgloss.Wrap(text, width, ""), "\n")[0]
}

func clampIndex(index, length int) int {
	if index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

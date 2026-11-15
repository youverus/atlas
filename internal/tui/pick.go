package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Choice is one row of a list the reader is asked to choose from: what the row
// says, and the value the host gets back when it is taken.
//
// The split is the one Suggestion makes: the interface places a label and a
// note, and Value stays opaque. Nothing here knows that one row is a model and
// the next is a session, which is what keeps this file free of the host's
// vocabulary.
type Choice struct {
	Value string // what the host gets back, unchanged
	Label string // the name, left column
	Note  string // the fact that tells two similar rows apart, middle column

	// Steps is the row's own horizontal axis: the positions ←/→ move through,
	// in the order the host wants them. A model can offer fewer of them than
	// its neighbour, so this belongs to the row and not to the list. Rows with
	// nothing to adjust leave it nil and the column collapses.
	Steps []string
	// Step is the position on that axis in effect when the list opens. The
	// interface owns it from then on, so it moves as the reader presses.
	Step int
}

// PickRequest is one question: the title over the list, the rows, the row the
// list opens on, and the word for the axis some rows carry.
type PickRequest struct {
	Title string
	Items []Choice
	// Current is the value already in effect. The list opens on that row, so
	// the menu answers "where am I" as well as "where else can I go". An empty
	// value, or one that is not in the list, opens on the first row.
	Current string
	// Axis names the horizontal dimension ←/→ moves a row along ("thinking",
	// "level", …). Only the footer's word for it: an empty axis means no row
	// has one and the keys are not advertised.
	Axis string
}

// PickMsg asks the reader to take one row. The goroutine that asked is blocked
// on Reply, so exactly one answer is sent.
type PickMsg struct {
	// Request is titled and spelled by the host: the interface supplies the
	// placement, never the words.
	Request PickRequest
	Reply   chan PickAnswer
}

// PickAnswer is the row that was taken, or nothing. A dismissed list and a list
// nobody could answer mean the same thing to the caller: there is nothing to do
// with the answer, so the command returns without changing anything.
type PickAnswer struct {
	Value string
	// Step is where the taken row's axis was left. A row without one answers 0,
	// so a caller that only asks "which row" can ignore it.
	Step  int
	Taken bool
}

const (
	// pickHint is the footer: the whole answer to "how do I work this". The
	// second spelling appears only when a row carries an axis, because a key
	// that does nothing is worse than no key at all.
	//
	// The words are as short as they are because three facts have to fit in one
	// line at the default box width of 72 cells: a footer that got truncated
	// would lose "Esc leaves", which is the one key a reader needs when they do
	// not want the answer.
	pickHint     = "↑↓ choose · type to filter · Enter takes · Esc leaves"
	pickHintAxis = "↑↓ choose · ←→ %s · type to filter · Enter takes · Esc leaves"

	// pickHintEmpty is what is left of the footer when the query matched
	// nothing: there is no row to move to and none to take, so advertising
	// those keys would be describing a list that is not on screen.
	pickHintEmpty = "Backspace widens it · Esc leaves"

	// pickHintBarren is the same case one step further out: the host gave a
	// list with nothing in it at all, so there is nothing to widen either and
	// leaving is the only thing that works.
	pickHintBarren = "Esc leaves"
)

// pickTitle is the box's first line: which list this is, where its window sits
// in it, and what the query left.
//
// The two counts are separate on purpose. The window is always "a–b of n" over
// the rows being browsed, and the query adds "n of total matching", so a reader
// who typed a word can tell how much of the list is behind them without
// clearing it to find out.
func pickTitle(base string, matched, total, first, visible int, query string) string {
	title := strings.TrimSpace(base)
	if title == "" {
		title = "choose"
	}
	if query == "" {
		if visible < total {
			return fmt.Sprintf("%s · %d–%d of %d", title, first+1, first+visible, total)
		}
		return title
	}
	// Nothing matched: the body says so, and a count of zero in the title would
	// only repeat it.
	if matched == 0 {
		return title
	}
	if visible < matched {
		title = fmt.Sprintf("%s · %d–%d of %d", title, first+1, first+visible, matched)
	}
	return fmt.Sprintf("%s · %d of %d matching %q", title, matched, total, query)
}

// pickBox draws one menu: a title, the rows that fit, and the keys that answer
// it. width is the box including its borders, height the rows it may occupy,
// and the layout's MenuRows bounds the list so a menu cannot grow into a second
// screen.
//
// shown is the rows the query left, in the host's order; chosen indexes that
// slice, and query is what the reader typed. The full list is still reachable —
// msg.Request.Items is where the host's rows live and where the counts come
// from — but the box never draws a row the query dropped.
func pickBox(msg *PickMsg, shown []Choice, chosen int, query string, height, width int, s Styles, l Layout) []string {
	if width < 12 {
		width = 12
	}
	// The box is a panel above the conversation, so every slot it uses is
	// rebuilt on the raised surface: a screen style would leave the screen's
	// color showing through every letter and gap.
	s = s.Panel()
	inner := width - 2 // between the vertical borders
	body := inner - 2  // one cell of padding on each side

	// A box is two borders, the title and the hint. What is left is the list,
	// which must keep at least one row: a box with nothing in its middle cannot
	// be read.
	rows := height - 4
	if rows > l.MenuRows {
		rows = l.MenuRows
	}
	if rows < 1 {
		rows = 1
	}
	visible, row, first := listWindow(shown, chosen, rows)

	title := pickTitle(msg.Request.Title, len(shown), len(msg.Request.Items), first, len(visible), query)

	label := labelColumn(visible, func(item Choice) string { return item.Label })
	// The axis is reserved at the right edge before the note, because it is the
	// only column that changes as the reader presses. When the box is too
	// narrow to hold all three, the axis goes first: a turned knob with nowhere
	// to put its label is not worth the row it costs.
	axis := axisWidth(visible)
	if axis > 0 && body-listMarker-axisGap(axis)-minAboutWidth < label {
		axis = 0
	}
	if limit := body - listMarker - axisGap(axis); label > limit {
		label = limit
	}
	if label < 0 {
		label = 0
	}
	noteWidth := noteColumn(body, label) - axisGap(axis)

	hint := pickHint
	switch {
	case len(visible) == 0 && query != "":
		// A query with no match is a state of its own: the keys that move
		// through rows and take one have nothing to act on, so the footer
		// offers the only two that still do something.
		hint = pickHintEmpty
	case len(visible) == 0:
		// The host handed over a list with nothing in it. Nothing here can
		// widen it, so the footer only says how to get out.
		hint = pickHintBarren
	case axis > 0 && msg.Request.Axis != "":
		hint = fmt.Sprintf(pickHintAxis, msg.Request.Axis)
	}

	// Room for the two borders, the title, the hint, and the line that stands in
	// for the rows when there are none.
	out := make([]string, 0, len(visible)+5)
	out = append(out, s.Border.Render("╭"+strings.Repeat("─", inner)+"╮"))
	out = append(out, boxRow(s.Accent.Bold(true).Render(cutCells(title, body)), body, s))
	if len(visible) == 0 {
		// An empty panel reads as a list that failed to load. One line saying
		// what happened is the difference between "no match" and "broken".
		empty := "nothing to choose here"
		if query != "" {
			empty = fmt.Sprintf("nothing matches %q", query)
		}
		out = append(out, boxRow(s.Dim.Render(cutCells(empty, body)), body, s))
	}
	for i, item := range visible {
		note := item.Note
		if axis > 0 && note != "" {
			// The axis follows, so this column has to be as wide as declared
			// or the rows stop lining up.
			note = padRight(cutCells(note, noteWidth), noteWidth)
		}
		line := listRow(i == row, item.Label, note, label, noteWidth, s.Text, s)
		if axis > 0 {
			line += s.Background.Render(strings.Repeat(" ", listColumnGap)) + axisCell(item, i == row, axis, s)
		}
		out = append(out, boxRow(line, body, s))
	}
	out = append(out, boxRow(s.Dim.Render(cutCells(hint, body)), body, s))
	out = append(out, s.Border.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return out
}

// choiceMatches reports whether one row answers every word of the query.
//
// The label and the note are what a row can be found by. Value is not: the
// interface promised the host it would not read it, and reading it here would
// make a session menu match on its path — typing the project's directory name
// would then match every session in it, which is the opposite of narrowing.
func choiceMatches(item Choice, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(item.Label + " " + item.Note)
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// queryTerms splits what the reader typed into the words every match must
// contain, lowercased. Words rather than the whole string because a model is
// remembered by two of them ("sonnet anthropic") and a session by any of the
// words in its preview.
func queryTerms(query string) []string {
	return strings.Fields(strings.ToLower(query))
}

// axisWidth is the column the axis needs, marks included, or 0 when no visible
// row has one.
func axisWidth(visible []Choice) int {
	widest := 0
	for _, item := range visible {
		for _, step := range item.Steps {
			if cells := lipgloss.Width(step); cells > widest {
				widest = cells
			}
		}
	}
	if widest == 0 {
		return 0
	}
	// Room for "‹ " and " ›", so the marks never push the label around.
	return widest + 4
}

// axisGap is what the axis costs beside the note, space before it included.
func axisGap(axis int) int {
	if axis == 0 {
		return 0
	}
	return axis + 2
}

// axisCell draws one row's position on its axis. The row that owns the keys is
// wrapped in guillemets; the others print the same word plain, right-aligned in
// the same column, so nothing shifts as the highlight moves and the marks say
// where the arrows act.
func axisCell(item Choice, selected bool, width int, s Styles) string {
	if len(item.Steps) == 0 {
		return strings.Repeat(" ", width)
	}
	label := item.Steps[clampIndex(item.Step, len(item.Steps))]
	if !selected {
		return s.Dim.Render(padLeft(cutCells(label, width), width))
	}
	shown := padLeft(cutCells("‹ "+label+" ›", width), width)
	return rowLabelStyle(selected, s.Text, s).Render(shown)
}

// boxRow frames one line of a box. The frame style and the padding live here
// rather than in each box, so the approval question and the menu cannot drift
// into two slightly different edges. The air on both sides and the padding are
// painted with the surface's background rather than left blank: a cell's color
// comes from what was written into it, and a bare space would let the screen
// show through the panel. The rendered row paints its own cells; its styles
// already carry the surface color, which is what Panel hands out.
func boxRow(rendered string, body int, s Styles) string {
	gap := body - lipgloss.Width(rendered)
	if gap < 0 {
		gap = 0
	}
	return s.Border.Render("│") + s.Background.Render(" ") + rendered + s.Background.Render(strings.Repeat(" ", gap)) + s.Background.Render(" ") + s.Border.Render("│")
}

// sendPick hands one answer back. The channel holds one value, so a reply that
// nobody is waiting for is dropped instead of blocking the interface.
func sendPick(reply chan PickAnswer, answer PickAnswer) {
	if reply == nil {
		return
	}
	select {
	case reply <- answer:
	default:
	}
}

// wrapIndex moves through a ring, so Down on the last row lands on the first.
// The menu and the command palette wrap the same way because they are the same
// gesture: a list that stopped at either end would make one of the two feel
// broken.
func wrapIndex(index, length int) int {
	if length < 1 {
		return 0
	}
	index %= length
	if index < 0 {
		index += length
	}
	return index
}

// stepIndex moves along a row's axis and stops at the ends. It does not wrap,
// unlike the rows themselves: the axis is a scale, not a ring — running off
// "max" and landing on "off" would turn one press of "more" into "none".
func stepIndex(index, length, delta int) int {
	if length < 1 {
		return 0
	}
	index += delta
	if index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

package tui

import (
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	xansi "github.com/charmbracelet/x/ansi"

	"pi-go/internal/theme"
)

// markdown draws an answer as the text it is written in rather than as the
// characters it is written with. A reply arrives in markdown, and a terminal
// that shows the asterisks and the pipes makes the reader do the parsing: the
// tables stop being tables and the code stops being code.
//
// It is a struct rather than a function because rendering is not free. Rows are
// rebuilt once per frame from every block, and a parse of a long reply costs on
// the order of a millisecond: a conversation with twenty answers in it would
// spend most of a frame re-deriving text that has not changed since the last
// keystroke. The cache below makes a block be parsed once per width and theme
// instead, and holding the renderer keeps the other half of that cost off the
// frame as well.
//
// Only answers come through here. What the reader typed is shown as typed, a
// tool result already has its own shape, and reasoning is a footnote: markdown
// in any of them would be a second interpretation of text the interface did not
// write.
type markdown struct {
	// entries holds the rows of every answer drawn so far, by block index. The
	// index is the key rather than the text because two answers can hold the
	// same words and still be two different blocks; the text is compared on
	// lookup because that is what changes while a reply streams.
	entries map[int]markdownRows

	// renderer is the glamour renderer built for width and palette, and nil
	// until an answer needs one.
	renderer *glamour.TermRenderer
	width    int
	palette  theme.Palette

	// parses counts how many times a block had to go through the parser. A
	// test reads it to prove the cache does what it claims, which is otherwise
	// invisible from the outside: a frame looks the same either way.
	parses int
}

// markdownRows is one answer's rows plus the whole of what shaped them, so a
// cache hit cannot hand back rows drawn for another width, another theme, or
// words that have grown since.
type markdownRows struct {
	width   int
	palette theme.Palette
	text    string
	lines   []string
}

// lines returns the rows for one answer at width: the cached ones when nothing
// that shapes them has moved, freshly parsed ones otherwise.
func (m *markdown) lines(index int, text string, width int, palette theme.Palette) []string {
	if text == "" {
		return nil
	}
	if rows, ok := m.entries[index]; ok && rows.width == width && rows.palette == palette && rows.text == text {
		return rows.lines
	}
	rendered := m.render(text, width, palette)
	if m.entries == nil {
		m.entries = make(map[int]markdownRows)
	}
	m.entries[index] = markdownRows{width: width, palette: palette, text: text, lines: rendered}
	return rendered
}

// forget drops every cached answer, which is what switching sessions does: the
// blocks those rows belonged to are gone, so the indices that named them would
// only ever miss from here on.
func (m *markdown) forget() { m.entries = nil }

// render parses one answer. A failure is not worth losing the answer over: the
// words are drawn unwrapped instead, which is what the interface did before any
// of this existed.
func (m *markdown) render(text string, width int, palette theme.Palette) []string {
	renderer := m.termRenderer(width, palette)
	if renderer == nil {
		return wrapText(text, width)
	}
	m.parses++
	rendered, err := renderer.Render(expandTabs(text))
	if err != nil {
		return wrapText(text, width)
	}
	return cleanAnswer(rendered, width)
}

// expandTabs spells a tab out as the four cells it usually stands for, before
// anything measures the text rather than after.
//
// A tab is one byte and, on screen, one to eight cells, and the parser takes
// the byte: a line with three tabs in it wraps as if it were three cells wide
// and is then four times that. Doing it here is what makes the wrapping right
// the first time; doing it afterwards only makes the row too wide, and a row
// too wide has to be cut, which loses words. Four spaces is also what markdown
// itself reads a tab as, so this is the same document either way.
func expandTabs(text string) string {
	if !strings.ContainsRune(text, '\t') {
		return text
	}
	return strings.ReplaceAll(text, "\t", "    ")
}

// termRenderer is the renderer for this width and palette, built on demand. A
// palette is comparable and so is a width, which is what makes "the same
// renderer" a question with an answer rather than a guess.
func (m *markdown) termRenderer(width int, palette theme.Palette) *glamour.TermRenderer {
	if m.renderer != nil && m.width == width && m.palette == palette {
		return m.renderer
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle(palette, width)),
		// The wrap width is the answer's own, not the screen's: the bar has
		// already taken its cells off the top.
		glamour.WithWordWrap(width),
		// An answer keeps the hard breaks it was written with. Reflowing them
		// would join two lines the author separated on purpose — an address, a
		// stanza, a row of a table that is not a table.
		glamour.WithPreservedNewLines(),
		// A table has no width of its own. Drawn to fit it stays a table; drawn
		// and then cut it becomes a row of leftovers.
		glamour.WithTableWrap(true),
	)
	if err != nil {
		return nil
	}
	m.renderer, m.width, m.palette = renderer, width, palette
	return renderer
}

// markdownStyle maps the theme onto glamour's vocabulary.
//
// Every entry names a token the rest of the screen already paints with, so an
// answer looks like the interface around it: no color is invented here, and a
// theme change reaches the answer the same way it reaches the frame — by
// rebuilding from the palette.
//
// The width is a parameter because one primitive has no width of its own: a
// horizontal rule is a run of cells, and how many is a property of the frame it
// is drawn in.
func markdownStyle(p theme.Palette, width int) ansi.StyleConfig {
	text := swatch(p.Text)
	dim := swatch(p.Dim)
	accent := swatch(p.Accent)
	code := swatch(p.Code)
	background := swatch(p.Background)

	// A heading has no size, so it is marked twice: in the accent color, which
	// separates it from the prose at a glance, and with the hashes it was
	// written with, which says which level it is on a terminal that has been
	// told to forget about color.
	heading := func(level int) ansi.StyleBlock {
		block := blockStyle(accent, background)
		block.Prefix = strings.Repeat("#", level) + " "
		block.Bold = pointer(true)
		return block
	}

	strong := spanStyle(text, background)
	strong.Bold = pointer(true)

	emphasis := spanStyle(text, background)
	emphasis.Italic = pointer(true)

	item := spanStyle(text, background)
	item.BlockPrefix = "• "

	// The number of an enumerated item is glamour's to write; this is what goes
	// between it and the words.
	enumeration := spanStyle(text, background)
	enumeration.BlockPrefix = ". "

	link := spanStyle(accent, background)
	link.Underline = pointer(true)

	quote := blockStyle(dim, background)
	quote.Indent = pointer(uint(1))
	quote.IndentToken = pointer("│ ")

	fence := ansi.StyleCodeBlock{StyleBlock: blockStyle(code, background)}
	// One cell of margin: a fenced block is a different thing from the sentence
	// above it, and on a screen with no background to put behind it, the indent
	// is the only thing that says so.
	fence.Margin = pointer(uint(1))

	rule := spanStyle(dim, background)
	rule.Format = "\n" + strings.Repeat("─", width) + "\n"

	document := blockStyle(text, background)
	// No document margin: the rows are already indented by the answer's bar,
	// and a second indent would eat four cells of every line to say nothing.
	document.Margin = pointer(uint(0))

	return ansi.StyleConfig{
		Document:       document,
		H1:             heading(1),
		H2:             heading(2),
		H3:             heading(3),
		H4:             heading(4),
		H5:             heading(5),
		H6:             heading(6),
		Emph:           emphasis,
		Strong:         strong,
		HorizontalRule: rule,
		Item:           item,
		Enumeration:    enumeration,
		List:           ansi.StyleList{LevelIndent: 2},
		Code:           blockStyle(code, background),
		CodeBlock:      fence,
		Table:          ansi.StyleTable{StyleBlock: blockStyle(text, background)},
		BlockQuote:     quote,
		Link:           link,
		LinkText:       link,
	}
}

// spanStyle is one styled run of text: a foreground, and the background of the
// surface it sits on.
//
// The background is not decoration. Every span glamour writes ends with a
// reset, and a reset drops the cell back to the terminal's own color: on a
// theme whose screen color is not the terminal's, an answer would read as text
// with holes punched in the surface behind it. Painting the background into the
// span is what keeps the answer on the same surface as everything else.
func spanStyle(foreground, background string) ansi.StylePrimitive {
	primitive := ansi.StylePrimitive{}
	if foreground != "" {
		primitive.Color = pointer(foreground)
	}
	if background != "" {
		primitive.BackgroundColor = pointer(background)
	}
	return primitive
}

// blockStyle is spanStyle plus the fields a block-level style carries.
func blockStyle(foreground, background string) ansi.StyleBlock {
	return ansi.StyleBlock{StylePrimitive: spanStyle(foreground, background)}
}

// swatch is a theme color as glamour spells one. The empty string means "the
// terminal's own color", which is exactly what a nil pointer does: a palette
// that left a token to the terminal should leave it to the terminal here too.
func swatch(c theme.Color) string {
	if c.Default() {
		return ""
	}
	return c.Hex()
}

// pointer is how glamour takes an attribute: every one of them is set or unset,
// and unset is nil rather than a zero value, because a style is composed by
// overwriting what the parent said.
func pointer[T any](value T) *T { return &value }

// emptySpan is a style sequence immediately followed by a reset: what is left
// of a span once the text it was written for is gone. It is how the cut
// padding ends, and it is also how glamour separates two neighbors that want
// the same look. Either way it covers no cells, and dropping it is what keeps
// a row's bytes proportional to what the reader can see.
var emptySpan = regexp.MustCompile("(?:\x1b\\[[0-9;]*m\x1b\\[m)+")

// cleanAnswer turns one rendered document into rows the transcript can draw: no
// trailing blank rows, no padding, and no gap at the top.
//
// The padding is the part that matters. glamour pads every row out to the wrap
// width, and space is a cell like any other: drawn into the answer they would
// make each row claim the full screen, so a selection would highlight the air
// beside the words, a copy would carry trailing blanks, and the bar the rows are
// drawn behind would be the only thing actually ending where the text does. The
// width is the caller's for that reason: it is the last word on how wide a row
// may be, and glamour's padding is not a reason to disagree with it.
func cleanAnswer(rendered string, width int) []string {
	rows := strings.Split(rendered, "\n")
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, fitRow(row, width))
	}
	// The transcript puts its own air between blocks, so a document that starts
	// with a blank row would draw two.
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	return lines
}

// fitRow cuts a row back to the cells it uses. A row of only padding is an
// empty row: what is left of it draws as nothing, so it is spelled as nothing.
func fitRow(row string, width int) string {
	plain := xansi.Strip(row)
	used := strings.TrimRight(plain, " ")
	if used == "" {
		return ""
	}
	cells := xansi.StringWidth(used)
	if cells > width {
		// Not reachable through word wrap, which is the point of asking: a
		// row wider than the frame pushes the right edge off the screen, and
		// no rendering mistake is worth that.
		cells = width
	}
	return emptySpan.ReplaceAllString(xansi.Truncate(row, cells, ""), "")
}

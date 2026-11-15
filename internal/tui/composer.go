package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Composer is the input line: one logical line that wraps visually as it grows,
// plus the history of what was submitted.
//
// It is deliberately not a text editor. The inline interface had one because it
// owned the bottom of the scrollback and a box to redraw inside; here the redraw
// is the terminal's job, so what is left is a rune buffer, a cursor, and the
// handful of keys a chat needs.
type Composer struct {
	buf []rune
	cur int

	history []string
	// hist is the history index being browsed, or -1 while the live buffer is
	// being edited. draft keeps that buffer aside so moving away and back does
	// not lose a half-typed line.
	hist  int
	draft []rune
}

// NewComposer returns an empty composer.
func NewComposer() Composer {
	return Composer{hist: -1}
}

// String is the buffer as it would be submitted.
func (c *Composer) String() string { return string(c.buf) }

// Empty reports whether there is nothing to submit.
func (c *Composer) Empty() bool { return len(strings.TrimSpace(string(c.buf))) == 0 }

// Len reports the buffer length in runes.
func (c *Composer) Len() int { return len(c.buf) }

// Set replaces the buffer and puts the cursor at the end. History browsing is
// cancelled: a seed is the new starting point, not a detour from the old one.
func (c *Composer) Set(text string) {
	c.buf = []rune(text)
	c.cur = len(c.buf)
	c.hist = -1
	c.draft = nil
}

// Clear empties the buffer.
func (c *Composer) Clear() {
	c.buf = c.buf[:0]
	c.cur = 0
	c.hist = -1
	c.draft = nil
}

// Insert adds text at the cursor. A newline becomes a space: Enter submits, so
// a multi-line paste has no way to stay multi-line here, and dropping the break
// is the only reading that keeps the words.
func (c *Composer) Insert(text string) {
	if text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	added := []rune(text)
	buf := make([]rune, 0, len(c.buf)+len(added))
	buf = append(buf, c.buf[:c.cur]...)
	buf = append(buf, added...)
	buf = append(buf, c.buf[c.cur:]...)
	c.buf = buf
	c.cur += len(added)
}

// Backspace deletes the rune before the cursor and reports whether it did.
func (c *Composer) Backspace() bool {
	if c.cur == 0 {
		return false
	}
	c.buf = append(c.buf[:c.cur-1], c.buf[c.cur:]...)
	c.cur--
	return true
}

// Delete removes the rune under the cursor.
func (c *Composer) Delete() bool {
	if c.cur >= len(c.buf) {
		return false
	}
	c.buf = append(c.buf[:c.cur], c.buf[c.cur+1:]...)
	return true
}

// Left moves the cursor one rune back.
func (c *Composer) Left() bool {
	if c.cur == 0 {
		return false
	}
	c.cur--
	return true
}

// Right moves the cursor one rune forward.
func (c *Composer) Right() bool {
	if c.cur >= len(c.buf) {
		return false
	}
	c.cur++
	return true
}

// Home puts the cursor at the start.
func (c *Composer) Home() { c.cur = 0 }

// End puts the cursor at the end.
func (c *Composer) End() { c.cur = len(c.buf) }

// DeleteWord removes the word before the cursor, which is the one editing
// gesture worth having on a line people type shell commands into.
func (c *Composer) DeleteWord() bool {
	start := c.cur
	for start > 0 && c.buf[start-1] == ' ' {
		start--
	}
	for start > 0 && c.buf[start-1] != ' ' {
		start--
	}
	if start == c.cur {
		return false
	}
	c.buf = append(c.buf[:start], c.buf[c.cur:]...)
	c.cur = start
	return true
}

// Take returns the submitted line and empties the composer. Blank lines are not
// submitted: they are the reader pressing Enter on an empty prompt.
func (c *Composer) Take() (string, bool) {
	line := strings.TrimRight(string(c.buf), " ")
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	if len(c.history) == 0 || c.history[len(c.history)-1] != line {
		c.history = append(c.history, line)
	}
	c.Clear()
	return line, true
}

// Previous walks back through the history.
func (c *Composer) Previous() bool {
	if len(c.history) == 0 {
		return false
	}
	if c.hist == -1 {
		c.draft = append([]rune(nil), c.buf...)
		c.hist = len(c.history) - 1
	} else if c.hist > 0 {
		c.hist--
	} else {
		return false
	}
	c.replace(c.history[c.hist])
	return true
}

// Next walks forward again, ending at the buffer that was being typed.
func (c *Composer) Next() bool {
	if c.hist == -1 {
		return false
	}
	if c.hist < len(c.history)-1 {
		c.hist++
		c.replace(c.history[c.hist])
		return true
	}
	c.hist = -1
	c.buf = append([]rune(nil), c.draft...)
	c.cur = len(c.buf)
	c.draft = nil
	return true
}

func (c *Composer) replace(text string) {
	c.buf = []rune(text)
	c.cur = len(c.buf)
}

// Lines renders the composer at width, each row prefixed by prefix. The prefix
// goes on the first row only, so a wrapped line reads as one entry rather than
// as several.
func (c *Composer) Lines(prefix string, width int, promptStyle, textStyle lipgloss.Style, placeholder string) []string {
	rows := c.layout(prefix, width)
	out := make([]string, 0, len(rows))
	if len(c.buf) == 0 && placeholder != "" {
		return []string{promptStyle.Render(prefix) + promptStyle.Render(placeholder)}
	}
	for i, row := range rows {
		if i == 0 {
			out = append(out, promptStyle.Render(prefix)+textStyle.Render(string(row)))
			continue
		}
		out = append(out, strings.Repeat(" ", lipgloss.Width(prefix))+textStyle.Render(string(row)))
	}
	return out
}

// Cursor reports where the caret is drawn, relative to the first row of Lines.
func (c *Composer) Cursor(prefix string, width int) (row, col int) {
	rows := c.layout(prefix, width)
	left := lipgloss.Width(prefix)

	remaining := c.cur
	for index, line := range rows {
		switch {
		case remaining < len(line):
			return index, clampColumn(left+cells(line[:remaining]), width)
		case remaining == len(line):
			// A caret at the end of a full row belongs at the start of the next
			// one: keeping it on the last column is where a terminal decides to
			// wrap by itself, which leaves the column ambiguous.
			if index < len(rows)-1 {
				return index + 1, left
			}
			return index, clampColumn(left+cells(line), width)
		}
		remaining -= len(line)
	}
	return len(rows) - 1, left
}

// clampColumn keeps the caret inside the frame. The last column is reserved for
// the wrap the terminal would otherwise do on its own.
func clampColumn(col, width int) int {
	if col >= width && width > 0 {
		return width - 1
	}
	return col
}

// layout breaks the buffer into rows of at most cols cells. Wrapping is by
// cells, not by words: a path with no spaces is one word, and breaking it is
// the only way to keep it on screen.
func (c *Composer) layout(prefix string, width int) [][]rune {
	cols := c.columns(prefix, width)
	if len(c.buf) == 0 {
		return [][]rune{{}}
	}
	var rows [][]rune
	row := make([]rune, 0, cols)
	used := 0
	for _, r := range c.buf {
		w := cells([]rune{r})
		if w < 1 {
			w = 1
		}
		if used+w > cols && len(row) > 0 {
			rows = append(rows, row)
			row = make([]rune, 0, cols)
			used = 0
		}
		row = append(row, r)
		used += w
	}
	rows = append(rows, row)
	return rows
}

func (c *Composer) columns(prefix string, width int) int {
	cols := width - lipgloss.Width(prefix)
	if cols < 1 {
		cols = 1
	}
	return cols
}

// cells measures text the way the terminal draws it. Wide characters take two
// columns, which is why the cursor position cannot be a byte or rune count.
func cells(runes []rune) int {
	if len(runes) == 0 {
		return 0
	}
	return lipgloss.Width(string(runes))
}

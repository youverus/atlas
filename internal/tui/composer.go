package tui

import (
	"slices"
	"strings"
	"unicode"

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

	images  []imagePick
	history []composerDraft
	// hist is -1 while editing the live draft. Keep it aside when browsing history.
	hist  int
	draft composerDraft
}

// Prompt is the submitted text and the images explicitly selected in it.
type Prompt struct {
	Text   string
	Images []string
}

type composerDraft struct {
	text   string
	images []imagePick
}

type imagePick struct {
	start, end int
	path       string
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
	c.replace(composerDraft{text: text})
	c.hist = -1
	c.draft = composerDraft{}
}

// Clear empties the buffer.
func (c *Composer) Clear() { c.Set("") }

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
	c.edit(c.cur, c.cur, []rune(text))
}

// edit shifts untouched selections and drops selections whose path was edited.
func (c *Composer) edit(start, end int, text []rune) {
	delta := len(text) - (end - start)
	for i := 0; i < len(c.images); {
		image := &c.images[i]
		switch {
		case image.start >= end:
			image.start += delta
			image.end += delta
		case image.end > start:
			c.images = slices.Delete(c.images, i, i+1)
			continue
		}
		i++
	}
	c.buf = slices.Replace(c.buf, start, end, text...)
	c.cur = start + len(text)
	c.images = slices.DeleteFunc(c.images, func(image imagePick) bool {
		return image.end < len(c.buf) && !unicode.IsSpace(c.buf[image.end]) ||
			image.start > 0 && !referenceBoundary(c.buf[image.start-1])
	})
}

// referenceBoundary permits a reference after whitespace or opening punctuation.
func referenceBoundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("([{<\"'`=", r)
}

// Backspace deletes the rune before the cursor and reports whether it did.
func (c *Composer) Backspace() bool {
	if c.cur == 0 {
		return false
	}
	c.edit(c.cur-1, c.cur, nil)
	return true
}

// Delete removes the rune under the cursor.
func (c *Composer) Delete() bool {
	if c.cur >= len(c.buf) {
		return false
	}
	c.edit(c.cur, c.cur+1, nil)
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

func (c *Composer) trimSpace() {
	start, end := 0, len(c.buf)
	for start < end && unicode.IsSpace(c.buf[start]) {
		start++
	}
	for start < end && unicode.IsSpace(c.buf[end-1]) {
		end--
	}
	c.edit(end, len(c.buf), nil)
	c.edit(0, start, nil)
	c.hist = -1
	c.draft = composerDraft{}
}

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
	c.edit(start, c.cur, nil)
	return true
}

// Take returns the submitted prompt and empties the composer.
func (c *Composer) Take() (Prompt, bool) {
	line := strings.TrimRight(string(c.buf), " ")
	if strings.TrimSpace(line) == "" {
		return Prompt{}, false
	}
	draft := composerDraft{text: line, images: slices.Clone(c.images)}
	if n := len(c.history); n == 0 || c.history[n-1].text != line || !slices.Equal(c.history[n-1].images, draft.images) {
		c.history = append(c.history, draft)
	}
	prompt := Prompt{Text: line}
	for _, image := range draft.images {
		if !slices.Contains(prompt.Images, image.path) {
			prompt.Images = append(prompt.Images, image.path)
		}
	}
	c.Clear()
	return prompt, true
}

// Previous walks back through the history.
func (c *Composer) Previous() bool {
	if len(c.history) == 0 {
		return false
	}
	if c.hist == -1 {
		c.draft = composerDraft{text: c.String(), images: slices.Clone(c.images)}
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
	c.replace(c.draft)
	c.draft = composerDraft{}
	return true
}

func (c *Composer) replace(draft composerDraft) {
	c.buf = []rune(draft.text)
	c.cur = len(c.buf)
	c.images = slices.Clone(draft.images)
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

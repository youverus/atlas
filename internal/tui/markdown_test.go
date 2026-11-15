package tui

import (
	"fmt"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"

	"pi-go/internal/theme"
)

// answer is the whole frame for one answer, which is what a screen draws when
// the conversation holds nothing else.
func answerRows(text string, width int, s Styles) []string {
	transcript := NewTranscript()
	transcript.Append(Block{Role: RoleAnswer, Text: text})
	return transcript.Lines(width, s, Layout{BlockGap: 1})
}

// text is a row as the terminal would show it: the escapes are what the
// interface writes, not what a reader sees.
func text(row string) string { return xansi.Strip(row) }

// wide reports the rows that claim more cells than the frame has, which is the
// one failure that pushes a row off the right edge.
func wide(t *testing.T, rows []string, width int) []string {
	t.Helper()
	var over []string
	for _, row := range rows {
		if got := xansi.StringWidth(row); got > width {
			over = append(over, fmt.Sprintf("%q is %d cells wide", text(row), got))
		}
	}
	return over
}

// sgrColor is the foreground sequence a row carries for one token, so a test
// can say "this is the code color" instead of spelling out a triple.
func sgrColor(t *testing.T, c theme.Color) string {
	t.Helper()
	r, g, b := hexTriplet(t, c.Hex())
	return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
}

// bolded reports whether the row styles this text bold. The attribute rides in
// the same sequence as the colors, so it has to be read out of the parameter
// list rather than looked for as a sequence of its own.
func bolded(row, word string) bool {
	for _, seq := range strings.Split(row, "\x1b[") {
		if !strings.Contains(seq, word) {
			continue
		}
		params, _, found := strings.Cut(seq, "m")
		if !found {
			continue
		}
		for _, param := range strings.Split(params, ";") {
			if param == "1" {
				return true
			}
		}
	}
	return false
}

// A table is the clearest thing the old renderer could not draw: as pipes and
// dashes it is unreadable, and as a drawn table it is the one shape that says
// the renderer understood the source rather than reproduced it.
func TestMarkdownDrawsATable(t *testing.T) {
	source := "| name | value |\n|---|---|\n| alpha | 1 |\n"
	rows := answerRows(source, 40, testStyles(t))

	if len(rows) < 3 {
		t.Fatalf("a three-row table drew %d rows: %q", len(rows), rows)
	}
	body := text(strings.Join(rows, "\n"))
	for _, want := range []string{"name", "value", "alpha"} {
		if !strings.Contains(body, want) {
			t.Errorf("the table lost %q: %q", want, body)
		}
	}
	if strings.Contains(body, "|---") || strings.Contains(body, "| ---") {
		t.Errorf("the table is still source: %q", body)
	}
	if !strings.Contains(body, "│") || !strings.Contains(body, "─") {
		t.Errorf("the table has no rule: %q", body)
	}
	if over := wide(t, rows, 40); len(over) > 0 {
		t.Errorf("a table row is wider than the frame: %s", strings.Join(over, "; "))
	}
}

// A fence is punctuation around code, and inside an answer it is noise that
// costs a row on each end.
func TestMarkdownDrawsCodeWithoutItsFence(t *testing.T) {
	s := resolvedStyles(t)
	rows := answerRows("```go\nfunc main() {}\n```\n", 40, s)

	body := text(strings.Join(rows, "\n"))
	if strings.Contains(body, "```") {
		t.Errorf("the fence survived: %q", body)
	}
	if !strings.Contains(body, "func main() {}") {
		t.Errorf("the code is missing: %q", body)
	}
	if !strings.Contains(strings.Join(rows, "\n"), sgrColor(t, s.palette.Code)) {
		t.Errorf("the code is not drawn in the code color: %q", rows)
	}
}

// Emphasis is the half of markdown a reader notices first: two asterisks are
// four cells of punctuation around a word that is already saying what it means.
func TestMarkdownStylesInlineText(t *testing.T) {
	s := resolvedStyles(t)
	rows := answerRows("some **bold** and `inline` text\n", 40, s)
	joined := strings.Join(rows, "\n")
	body := text(joined)

	for _, gone := range []string{"**", "`"} {
		if strings.Contains(body, gone) {
			t.Errorf("the row still carries %q: %q", gone, body)
		}
	}
	for _, want := range []string{"bold", "inline"} {
		if !strings.Contains(body, want) {
			t.Errorf("the row lost %q: %q", want, body)
		}
	}
	if !bolded(joined, "bold") {
		t.Errorf("bold is not bold: %q", rows)
	}
	if !strings.Contains(joined, sgrColor(t, s.palette.Code)) {
		t.Errorf("inline code is not the code color: %q", rows)
	}
}

// glamour pads every row out to the wrap width. Space is a cell: left in, a
// selection would highlight air and the bar would be the only thing ending
// where the words end.
func TestMarkdownRowsEndWhereTheirTextEnds(t *testing.T) {
	s := resolvedStyles(t)
	rows := answerRows("# Short\n\nA table:\n\n| a | b |\n|---|---|\n| 1 | 2 |\n", 40, s)

	for _, row := range rows {
		body := strings.TrimPrefix(text(row), AnswerBar)
		if body == "" {
			continue
		}
		if strings.HasSuffix(body, " ") {
			t.Errorf("row %q ends in padding", body)
		}
	}
}

// The whole reason the renderer and the cache live on a struct: Lines runs once
// per frame, and a frame that reparsed every answer would spend its time on
// text that has not changed since the last keystroke.
func TestMarkdownParsesOncePerWidth(t *testing.T) {
	transcript := NewTranscript()
	transcript.Append(Block{Role: RoleAnswer, Text: "one **answer** here\n"})
	transcript.Append(Block{Role: RoleAnswer, Text: "another `answer` there\n"})
	s := resolvedStyles(t)

	first := transcript.Lines(40, s, Layout{BlockGap: 1})
	if got := transcript.md.parses; got != 2 {
		t.Fatalf("first frame parsed %d answers, want 2", got)
	}
	second := transcript.Lines(40, s, Layout{BlockGap: 1})
	if got := transcript.md.parses; got != 2 {
		t.Errorf("second frame parsed %d answers, want the cache to answer both", got)
	}
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Errorf("the same frame twice drew differently:\n%q\n%q", first, second)
	}

	transcript.Lines(30, s, Layout{BlockGap: 1})
	if got := transcript.md.parses; got != 4 {
		t.Errorf("a new width parsed %d answers, want 4: rows are a function of it", got)
	}
}

// A theme change is the other thing rows are a function of, and the cache has
// to notice it for the same reason it notices a width.
func TestMarkdownReparsesForANewPalette(t *testing.T) {
	transcript := NewTranscript()
	transcript.Append(Block{Role: RoleAnswer, Text: "an answer\n"})
	transcript.Lines(40, resolvedStyles(t), Layout{BlockGap: 1})
	before := transcript.md.parses

	transcript.Lines(40, Styles{}, Layout{BlockGap: 1})
	if got := transcript.md.parses; got != before+1 {
		t.Errorf("a palette change parsed %d answers, want %d", got, before+1)
	}
}

// Narrow is where a renderer earns its place: the answer has to be wrapped once,
// to the width it is actually drawn at, rather than wrapped by markdown and then
// wrapped again by the block around it.
func TestMarkdownWrapsToTheWidthItIsGiven(t *testing.T) {
	source := "## Heading\n\nA paragraph long enough to need wrapping at any sensible width.\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	for _, width := range []int{12, 24, 40, 80} {
		rows := answerRows(source, width, testStyles(t))
		if over := wide(t, rows, width); len(over) > 0 {
			t.Errorf("width %d: %s", width, strings.Join(over, "; "))
		}
		for _, row := range rows {
			if row != "" && !strings.HasPrefix(text(row), AnswerBar) {
				t.Errorf("width %d: row %q lost its bar", width, text(row))
			}
		}
	}
}

// Everything that is not markdown has to survive being read as markdown, which
// is the property that makes this a drawing change rather than a text one.
func TestMarkdownLeavesPlainProseVerbatim(t *testing.T) {
	corpus := []string{
		"the answer",
		"done",
		"one two three four five six seven eight nine ten eleven twelve",
		"line one\nline two\nline three",
		"a  b   c    d",
		"中文中文中文中文中文中文中文中文中文中文中文中文",
		"mixed 中文 and ascii words that have to wrap somewhere near the edge",
		"supercalifragilisticexpialidociousandthensomemorelettershere",
		strings.Repeat("word ", 20) + "end",
	}
	for _, width := range []int{9, 20, 38, 80} {
		for _, source := range corpus {
			got := strings.Join(answerRows(source, width, Styles{}), "\n")
			want := strings.Join(answerBlock(Block{Role: RoleAnswer, Text: source}, width, Styles{}), "\n")
			if got != want {
				t.Errorf("width %d, %q:\n drew %q\n plain %q", width, source, got, want)
			}
		}
	}
}

// Only answers are markdown. A prompt is what the reader typed, a tool result
// is what a program printed, and reasoning is a footnote: reading any of them
// as markdown would be a second interpretation of text the interface did not
// write.
func TestMarkdownOnlyDrawsAnswers(t *testing.T) {
	source := "## not a heading\n\n- not a list\n"
	transcript := NewTranscript()
	transcript.Append(Block{Role: RoleUser, Text: source})
	transcript.Append(Block{Role: RoleThinking, Text: source})
	transcript.Append(Block{Role: RoleTool, Name: "bash", Text: source})
	transcript.Append(Block{Role: RoleError, Text: source})
	transcript.Append(Block{Role: RoleNotice, Text: source})

	body := text(strings.Join(transcript.Lines(60, testStyles(t), Layout{BlockGap: 1}), "\n"))
	if !strings.Contains(body, "## not a heading") {
		t.Errorf("a non-answer was read as markdown: %q", body)
	}
	if !strings.Contains(body, "- not a list") {
		t.Errorf("a non-answer lost its list source: %q", body)
	}
	if transcript.md != nil {
		t.Errorf("rendering only non-answers built a markdown renderer")
	}
}

// A heading has no size on a terminal, so it keeps the hashes it was written
// with: they are the only thing that says which level it is where color is not
// available.
func TestMarkdownKeepsHeadingLevels(t *testing.T) {
	s := resolvedStyles(t)
	for level := 1; level <= 6; level++ {
		hashes := strings.Repeat("#", level)
		rows := answerRows(hashes+" Title\n", 40, s)
		if len(rows) != 1 {
			t.Fatalf("h%d drew %d rows: %q", level, len(rows), rows)
		}
		if got, want := text(rows[0]), AnswerBar+hashes+" Title"; got != want {
			t.Errorf("h%d drew %q, want %q", level, got, want)
		}
		if !bolded(rows[0], "Title") {
			t.Errorf("h%d is not bold: %q", level, rows[0])
		}
	}
}

// An answer is drawn while it is still arriving, so every prefix of it has to
// draw as something: an unclosed fence is a code block, an unclosed asterisk is
// text, and a heading is a heading from its first space.
func TestMarkdownDrawsHalfWrittenAnswers(t *testing.T) {
	fragments := []string{
		"```go\nfunc main",
		"**bol",
		"| a | b",
		"## Ti",
		"- it",
		"a `code",
		"> quo",
	}
	for _, fragment := range fragments {
		rows := answerRows(fragment, 20, testStyles(t))
		if len(rows) == 0 {
			t.Errorf("%q drew nothing", fragment)
		}
		if over := wide(t, rows, 20); len(over) > 0 {
			t.Errorf("%q: %s", fragment, strings.Join(over, "; "))
		}
	}
}

// Switching sessions replaces every block, so the rows cached against their
// indices belong to a conversation that is no longer on screen.
func TestMarkdownForgetsRowsOnReset(t *testing.T) {
	transcript := NewTranscript()
	transcript.Append(Block{Role: RoleAnswer, Text: "an answer\n"})
	transcript.Lines(40, resolvedStyles(t), Layout{BlockGap: 1})
	before := transcript.md.parses

	transcript.Reset()
	transcript.Append(Block{Role: RoleAnswer, Text: "an answer\n"})
	transcript.Lines(40, resolvedStyles(t), Layout{BlockGap: 1})

	if got := transcript.md.parses; got != before+1 {
		t.Errorf("after a reset the answer parsed %d times, want %d", got-before, 1)
	}
}

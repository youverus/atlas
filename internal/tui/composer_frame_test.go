package tui

// 0.4.15 put the prompt inside a box on a raised background and painted the
// screen behind everything. These are the tests of that box: what it costs the
// screen, where the prompt lands inside it, and that a frame with no screen
// color behind it is still the frame it was.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"pi-go/internal/theme"
)

// composerBlockOf reads the rows the input area occupies off a frame, which is
// the only place a box means anything: the box is what the terminal gets.
func composerBlockOf(m Model) []string {
	frame := strings.Split(plain(m.View().Content), "\n")
	start := m.inputTop() + len(m.suggestionLines())
	end := start + len(m.composerBlock())
	if end > len(frame) {
		end = len(frame)
	}
	return frame[start:end]
}

// TestTheComposerSitsInsideAFullWidthBox: three rows — a rule, the prompt, a
// rule — each exactly as wide as the screen. A box narrower than the screen
// would leave the panel floating, and a box wider than it would be cut by the
// terminal, losing the right border.
func TestTheComposerSitsInsideAFullWidthBox(t *testing.T) {
	m := answered(t)
	block := composerBlockOf(m)
	if len(block) != 3 {
		t.Fatalf("the input area drew %d rows, want a rule, the prompt and a rule: %q", len(block), block)
	}
	for index, row := range block {
		if got := lipgloss.Width(row); got != m.width {
			t.Fatalf("row %d is %d cells wide, want the screen's %d: %q", index, got, m.width, row)
		}
	}
	if !strings.HasPrefix(block[0], "╭") || !strings.HasSuffix(block[0], "╮") {
		t.Fatalf("top row = %q, want two corners and a rule between them", block[0])
	}
	if !strings.HasPrefix(block[2], "╰") || !strings.HasSuffix(block[2], "╯") {
		t.Fatalf("bottom row = %q, want two corners and a rule between them", block[2])
	}
	// The prompt is inside the border and the air behind it, not on either.
	prompt := block[1]
	if !strings.HasPrefix(prompt, "│") || !strings.HasSuffix(prompt, "│") {
		t.Fatalf("prompt row = %q, want a border on both sides", prompt)
	}
	if column := runeIndexOf(prompt, '>'); column != composerBorder {
		t.Fatalf("the prompt starts at column %d, want %d inside the frame: %q", column, composerBorder, prompt)
	}
	// The box is two rows out of the screen's budget, not decoration added
	// afterwards: the frame still has to be exactly one screen tall, and its
	// last rule has to sit directly above the status row (and the gap that
	// belongs to it). Counting the rows the frame adds instead of letting it
	// push the status row off the bottom is the difference between a box and a
	// box that costs a row the terminal does not have.
	lines := strings.Split(plain(m.View().Content), "\n")
	if len(lines) != m.height {
		t.Fatalf("the frame is %d rows tall, want the screen's %d", len(lines), m.height)
	}
	end := m.inputTop() + len(m.composerBlock())
	if want := m.height - 1 - m.cfg.Layout.StatusGap; end != want {
		t.Fatalf("the box ends at row %d, want %d: a status row and its gap sit below it", end, want)
	}
}

// runeIndexOf is the column of needle in a row, counted in cells rather than
// bytes: the box's border is three bytes wide and one column wide, and a byte
// offset would report the prompt four columns in.
func runeIndexOf(row string, needle rune) int {
	column := 0
	for _, r := range row {
		if r == needle {
			return column
		}
		column += lipgloss.Width(string(r))
	}
	return -1
}

// TestTurningTheFrameOffIsTheScreenBeforeIt: the frame is geometry, so removing
// it has to give the two rows back and put the prompt back where it was. A
// flag that only stopped drawing would leave a hole the size of the box.
func TestTurningTheFrameOffIsTheScreenBeforeIt(t *testing.T) {
	framed := answered(t)
	plainBox := withLayout(t, func(l *Layout) { l.ComposerFrame = false }, testStyles(t), palette)

	if got, want := plainBox.bodyHeight(), framed.bodyHeight()+2; got != want {
		t.Fatalf("without the frame the transcript is %d rows, want %d back", got, want)
	}
	if got := len(plainBox.composerBlock()); got != len(plainBox.composerLines()) {
		t.Fatalf("the input area is %d rows with no frame, want the prompt's own %d", got, len(plainBox.composerLines()))
	}
	frame := strings.Split(plain(plainBox.View().Content), "\n")
	if strings.Contains(strings.Join(frame, "\n"), "╭") {
		t.Fatalf("a box is still drawn with the frame off:\n%s", strings.Join(frame, "\n"))
	}
	if got := frame[plainBox.inputTop()]; !strings.Contains(got, ">") {
		t.Fatalf("row %d = %q, want the prompt at the top of the input area", plainBox.inputTop(), got)
	}
	// The caret follows the same decision.
	if row, _, ok := plainBox.caret(); !ok || row != plainBox.inputTop() {
		t.Fatalf("caret row = %d, %v, want %d", row, ok, plainBox.inputTop())
	}
}

// TestANarrowScreenDropsTheBoxEntirely: below the minimum the border would eat
// the prompt it is there to frame, so it goes rather than shrinking into a box
// with no room for text.
func TestANarrowScreenDropsTheBoxEntirely(t *testing.T) {
	m := New(Config{Styles: testStyles(t), Layout: DefaultLayout(), Suggest: palette})
	// The minimum is a border, its air and four cells of text on each side.
	m = resize(t, m, 2*composerBorder+4, 24)
	if block := composerBlockOf(m); !strings.Contains(strings.Join(block, "\n"), "╭") {
		t.Fatalf("the box is missing at its own minimum width: %q", block)
	}
	m = resize(t, m, 2*composerBorder+3, 24)
	block := composerBlockOf(m)
	if got := strings.Join(block, "\n"); strings.Contains(got, "╭") || strings.Contains(got, "│") {
		t.Fatalf("a box is drawn one cell below the minimum:\n%s", got)
	}
	if len(block) != 1 {
		t.Fatalf("the input area drew %d rows with no box, want 1: %q", len(block), block)
	}
}

// TestThePromptWrapsInsideTheFrame: the box costs four columns, so what fits on
// one row inside it is exactly what fits on a screen four columns narrower.
// Measuring the wrap against the whole screen would run a line under the right
// border — and nothing else would look wrong, because the box pads itself to
// its own width and the corners still line up.
func TestThePromptWrapsInsideTheFrame(t *testing.T) {
	const width = 40
	for _, length := range []int{10, 20, 35, 36, 37, 50, 74, 120} {
		text := strings.Repeat("x", length)
		framed := typeInto(withLayout(t, func(*Layout) {}, testStyles(t), palette), text)
		framed = resize(t, framed, width, 24)
		bare := withLayout(t, func(l *Layout) { l.ComposerFrame = false }, testStyles(t), palette)
		bare = typeInto(bare, text)
		bare = resize(t, bare, width-2*composerBorder, 24)
		if got, want := len(framed.composerLines()), len(bare.composerLines()); got != want {
			t.Fatalf("%d characters: the box wraps into %d rows, want %d — the same text on a screen %d columns narrower", length, got, want, 2*composerBorder)
		}
	}
}

// typeInto is one keystroke carrying a whole string, which is what a paste is.
func typeInto(m Model, text string) Model {
	updated, _ := m.Update(typeText(text))
	return updated.(Model)
}

// resize asks the model for a screen of its own, which is what a terminal does
// when the window is dragged.
func resize(t *testing.T, m Model, width, height int) Model {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(Model)
}

// TestTheScreenIsPaintedFromEdgeToEdge is the point of the whole slice: every
// cell of the frame carries a background, so the interface is one surface
// instead of text floating on whatever the terminal was showing.
//
// The two halves are checked apart because they are two different colors: the
// conversation sits on the theme's background, and the input area sits on the
// raised one derived from it.
func TestTheScreenIsPaintedFromEdgeToEdge(t *testing.T) {
	// A model with the real palette: the zero-value styles every other test
	// uses paint nothing, which is exactly what this test is not about.
	m := withLayout(t, func(*Layout) {}, resolvedStyles(t), palette)
	library, err := theme.NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Only the numbers: lipgloss writes a cell's foreground and background into
	// one escape, so the exact bytes depend on what else the cell carries.
	screen := painted(t, "48", resolved.Palette.Background.Hex())
	surface := painted(t, "48", resolved.Surface().Hex())
	if screen == surface {
		t.Fatalf("the panel and the screen are the same color %q", screen)
	}

	start := m.inputTop() + len(m.suggestionLines())
	end := start + len(m.composerBlock())
	for index, row := range strings.Split(m.View().Content, "\n") {
		want := screen
		inBox := index >= start && index < end
		if inBox {
			want = surface
		}
		cells := cellBackgrounds(row)
		if len(cells) != m.width {
			t.Fatalf("row %d has %d cells, want %d: %q", index, len(cells), m.width, row)
		}
		for column, got := range cells {
			if got != want {
				t.Fatalf("row %d column %d carries background %q, want %q: %q", index, column, got, want, row)
			}
		}
	}
}

// cellBackgrounds is the background each cell of a row carries, as the SGR
// parameters that set it ("48;2;26;27;38") or "" for a cell left to the
// terminal. Reading the cells rather than the row is the difference between
// "this row mentions the color" and "this row is painted with it": a row can
// set the background around its text and leave the padding bare, and the eye
// sees a stripe down the middle of the panel.
// sgrParams is sgr with the parameters kept: painting is a question about what
// each escape says, not about where the text resumes.
var sgrParams = regexp.MustCompile("\x1b\\[([0-9;]*)m")

func cellBackgrounds(row string) []string {
	var out []string
	bg := ""
	add := func(text string) {
		for range text {
			out = append(out, bg)
		}
	}
	last := 0
	for _, match := range sgrParams.FindAllStringSubmatchIndex(row, -1) {
		add(row[last:match[0]])
		params := strings.Split(row[match[2]:match[3]], ";")
		for i := 0; i < len(params); i++ {
			switch {
			case params[i] == "" || params[i] == "0":
				bg = ""
			case params[i] == "48" && i+1 < len(params) && params[i+1] == "2" && i+4 < len(params):
				bg = strings.Join(params[i:i+5], ";")
				i += 4
			}
		}
		last = match[1]
	}
	add(row[last:])
	return out
}

// painted spells the numbers of one SGR attribute for a color: "48;2;26;27;38"
// for a background. Writing them out rather than reading them back from a
// style is what makes the assertion about the color instead of about the order
// lipgloss puts the attributes in.
func painted(t *testing.T, attribute, hex string) string {
	t.Helper()
	if hex == "" {
		t.Fatalf("no color to paint with")
	}
	r, g, b := hexTriplet(t, hex)
	return fmt.Sprintf("%s;2;%d;%d;%d", attribute, r, g, b)
}

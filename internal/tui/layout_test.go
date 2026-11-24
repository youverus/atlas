package tui

// 0.4.10 turned the geometry constants into a value the host hands over, and
// these are the tests of that value: that the numbers still mean what they meant
// as constants, and that a config file can move each of them.

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestAnOmittedLayoutIsTheInterfaceEverybodyKnows: a caller that has no opinion
// — every host that does not read a config, and every test — must get 0.4.9's
// screen, not an empty one. Omitting the field and spelling its zero value are
// the same statement, so one case covers both.
func TestAnOmittedLayoutIsTheInterfaceEverybodyKnows(t *testing.T) {
	m := New(Config{})
	if m.cfg.Layout != DefaultLayout() {
		t.Fatalf("layout = %+v, want the defaults", m.cfg.Layout)
	}
	// The defaults also have to be the old constants, or "the interface
	// everybody knows" would be a wish rather than a fact.
	defaults := DefaultLayout()
	if defaults.ComposerGap != 1 || defaults.BlockGap != 1 || defaults.ComposerRows != 8 ||
		defaults.PaletteRows != 5 || defaults.MenuRows != 10 || defaults.MenuWidth != 72 ||
		defaults.BoxWidth != 64 || defaults.ThinkingRows != 8 || !defaults.HomePage {
		t.Fatalf("DefaultLayout = %+v, want the geometry 0.4.9 drew", defaults)
	}
}

// TestALayoutIsTakenAsWritten pins the rule cmd/atlas depends on: starting from
// DefaultLayout and changing one field leaves the other eight alone. A literal
// is read literally, zeros included, which is why the host builds its layout the
// other way.
func TestALayoutIsTakenAsWritten(t *testing.T) {
	layout := DefaultLayout()
	layout.ComposerGap = 3
	m := New(Config{Layout: layout})
	if m.cfg.Layout.ComposerGap != 3 {
		t.Fatalf("composerGap = %d, want 3", m.cfg.Layout.ComposerGap)
	}
	if m.cfg.Layout.BlockGap != 1 || m.cfg.Layout.MenuWidth != 72 || !m.cfg.Layout.HomePage {
		t.Fatalf("changing one field moved another: %+v", m.cfg.Layout)
	}

	bare := New(Config{Layout: Layout{ComposerGap: 2}})
	if bare.cfg.Layout.BlockGap != 0 {
		t.Fatalf("blockGap = %d, want the zero a literal spells", bare.cfg.Layout.BlockGap)
	}
}

// TestALayoutThatCannotBeDrawnIsClamped is the defense on this side of the
// boundary: internal/config reports a number nobody meant, but a host that
// builds a Layout in code has no such reader, and the frame arithmetic cannot
// afford a negative row count.
func TestALayoutThatCannotBeDrawnIsClamped(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Layout: Layout{
		ComposerGap:  -4,
		BlockGap:     -1,
		ComposerRows: 0,
		PaletteRows:  -2,
		MenuRows:     0,
		MenuWidth:    -3,
		BoxWidth:     -3,
		ThinkingRows: -5,
	}}))
	layout := m.cfg.Layout
	if layout.ComposerGap != 0 || layout.BlockGap != 0 || layout.PaletteRows != 0 || layout.ThinkingRows != 0 {
		t.Fatalf("a negative count survived: %+v", layout)
	}
	if layout.ComposerRows != 1 || layout.MenuRows != 1 {
		t.Fatalf("a zero limit survived where one row is the minimum: %+v", layout)
	}
	if layout.MenuWidth < 12 || layout.BoxWidth < 12 {
		t.Fatalf("a box narrower than its own border survived: %+v", layout)
	}
	if lines := strings.Count(m.View().Content, "\n") + 1; lines != m.height {
		t.Fatalf("frame has %d rows, want %d", lines, m.height)
	}
}

// TestTheGapAboveThePromptComesFromTheLayout is the 0.4.7 gap, now a setting:
// zero puts the prompt straight under the conversation, and a larger number
// gives the answer more room to be read as an answer.
func TestTheGapAboveThePromptComesFromTheLayout(t *testing.T) {
	for _, gap := range []int{0, 2, 3} {
		m := withLayout(t, func(l *Layout) { l.ComposerGap = gap }, testStyles(t), nil)
		if got, want := m.inputTop(), m.bodyHeight()+gap; got != want {
			t.Fatalf("gap %d: input top = %d, want %d", gap, got, want)
		}
		frame := strings.Split(plain(m.View().Content), "\n")
		if len(frame) != m.height {
			t.Fatalf("gap %d: frame has %d rows, want %d", gap, len(frame), m.height)
		}
		for row := m.bodyHeight(); row < m.inputTop(); row++ {
			if got := strings.TrimSpace(frame[row]); got != "" {
				t.Fatalf("gap %d: row %d = %q, want blank", gap, row, got)
			}
		}
		// The frame is part of the input area: its top rule takes the first
		// row and the prompt is the row under it.
		top := m.inputTop() + m.cfg.Layout.composerTop(m.width)
		if got := frame[m.inputTop()]; !strings.Contains(got, "╭") {
			t.Fatalf("gap %d: row %d = %q, want the top of the input box", gap, m.inputTop(), got)
		}
		if got := frame[top]; !strings.Contains(got, ">") {
			t.Fatalf("gap %d: row %d = %q, want the prompt", gap, top, got)
		}
		// The caret reads the same helpers, so a gap cannot exist in the frame
		// and not in the cursor.
		if row, _, ok := m.caret(); !ok || row != top {
			t.Fatalf("gap %d: caret row = %d, %v, want %d", gap, row, ok, top)
		}
	}
}

// TestTheBlockGapComesFromTheLayout: the blank line between two blocks is the
// difference between prose and a wall, and it is the one spacing knob that shows
// up in the transcript rather than around it.
func TestTheBlockGapComesFromTheLayout(t *testing.T) {
	for _, gap := range []int{0, 1, 2} {
		layout := DefaultLayout()
		layout.BlockGap = gap
		tr := NewTranscript()
		tr.Append(Block{Role: RoleUser, Text: "a question"})
		tr.Append(Block{Role: RoleAnswer, Text: "the answer"})
		lines := tr.Lines(40, testStyles(t), layout)

		blank := 0
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				blank++
			}
		}
		if blank != gap {
			t.Fatalf("blockGap %d produced %d blank rows: %q", gap, blank, lines)
		}
	}
}

// TestTheComposerRowsComeFromTheLayout: a long line takes the room it is
// allowed and then scrolls, because a composer that grew without a ceiling would
// be the whole screen.
func TestTheComposerRowsComeFromTheLayout(t *testing.T) {
	line := strings.Repeat("wrap ", 60)
	for _, rows := range []int{1, 3} {
		m := sized(t, New(Config{Styles: testStyles(t), Layout: func() Layout {
			layout := DefaultLayout()
			layout.ComposerRows = rows
			return layout
		}()}))
		m.composer().Set(line)
		if got := len(m.composerLines()); got != rows {
			t.Fatalf("composerRows %d drew %d rows", rows, got)
		}
	}
}

// TestThePaletteRowsComeFromTheLayout: the command palette is a hint, so it is
// bounded, and zero is a reader who does not want the hint at all.
func TestThePaletteRowsComeFromTheLayout(t *testing.T) {
	for _, rows := range []int{0, 1, 2} {
		m := withLayout(t, func(l *Layout) { l.PaletteRows = rows }, testStyles(t), palette)
		updated, _ := m.Update(typeText("/"))
		m = updated.(Model)
		if got := len(m.suggestionLines()); got != rows {
			t.Fatalf("paletteRows %d drew %d rows", rows, got)
		}
	}
}

// TestTheMenuRowsComeFromTheLayout: the list inside a pick menu scrolls past its
// limit instead of growing, and the title keeps saying how many rows are behind
// the window.
func TestTheMenuRowsComeFromTheLayout(t *testing.T) {
	values := make([]string, 12)
	for i := range values {
		values[i] = "m" + strings.Repeat("x", i%3) + string(rune('a'+i))
	}
	for _, rows := range []int{2, 12} {
		m := withLayout(t, func(l *Layout) { l.MenuRows = rows }, testStyles(t), nil)
		updated, _ := m.Update(pickMenu("model", "", values...))
		m = updated.(Model)

		box := pickBox(m.chooser, m.pickRows(), m.chosen, m.query, m.height, 72, testStyles(t), m.cfg.Layout)
		// A box is two borders, a title and a hint around the list.
		if want := rows + 4; len(box) != want {
			t.Fatalf("menuRows %d drew %d rows, want %d", rows, len(box), want)
		}
		frame := plain(m.View().Content)
		if rows < len(values) && !strings.Contains(frame, "of 12") {
			t.Fatalf("menuRows %d hid the rest of the list without saying so:\n%s", rows, frame)
		}
	}
}

// TestTheBoxWidthsComeFromTheLayout: neither box may grow to the whole screen,
// which is what keeps the conversation behind them readable while a question
// is answered.
func TestTheBoxWidthsComeFromTheLayout(t *testing.T) {
	// The question.
	m := withLayout(t, func(l *Layout) { l.BoxWidth = 30 }, testStyles(t), nil)
	m.approval = &AskMsg{Request: AskRequest{Subject: "run the thing?", Tool: "bash"}, Reply: make(chan AskAnswer, 1)}
	if got := boxWidth(t, m); got != 30 {
		t.Fatalf("approval box is %d cells wide, want 30", got)
	}

	// The list.
	m = withLayout(t, func(l *Layout) { l.MenuWidth = 40 }, testStyles(t), nil)
	updated, _ := m.Update(pickMenu("model", "", "alpha", "beta"))
	m = updated.(Model)
	if got := boxWidth(t, m); got != 40 {
		t.Fatalf("menu box is %d cells wide, want 40", got)
	}
}

// TestTheBoxesSitAboveTheInputArea: the reader's eyes are on the prompt, so a
// question opens directly above it instead of in the middle of the frame, where
// it can go unnoticed. The rows the box replaces stay dropped and the input
// area is never covered.
func TestTheBoxesSitAboveTheInputArea(t *testing.T) {
	// checkBox asserts the box occupies the rows that end one row above the
	// input area, right-padding aside.
	checkBox := func(frame []string, box []string, top int) {
		t.Helper()
		for i, want := range box {
			if got := strings.TrimRight(frame[top+i], " "); plain(got) != plain(want) {
				t.Fatalf("row %d = %q, want %q", top+i, got, plain(want))
			}
		}
	}

	// The question, with the box wide enough to take the whole frame's margin.
	m := withLayout(t, func(l *Layout) { l.BoxWidth = 76 }, testStyles(t), nil)
	m.approval = &AskMsg{Request: AskRequest{Subject: "run the thing?", Tool: "bash"}, Reply: make(chan AskAnswer, 1)}
	frame := strings.Split(plain(m.View().Content), "\n")
	box := approvalBox(m.approval.Request, m.width-4, testStyles(t))
	checkBox(frame, box, m.inputTop()-len(box))
	if got := frame[m.inputTop()]; !strings.Contains(got, "╭") {
		t.Fatalf("row %d = %q, want the composer right under the box", m.inputTop(), got)
	}

	// The list.
	m = withLayout(t, func(l *Layout) { l.MenuWidth = 76 }, testStyles(t), nil)
	updated, _ := m.Update(pickMenu("model", "", "alpha", "beta"))
	m = updated.(Model)
	frame = strings.Split(plain(m.View().Content), "\n")
	box = pickBox(m.chooser, m.pickRows(), m.chosen, m.query, m.inputTop(), m.width-4, testStyles(t), m.cfg.Layout)
	checkBox(frame, box, m.inputTop()-len(box))
	if got := frame[m.inputTop()]; !strings.Contains(got, "╭") {
		t.Fatalf("row %d = %q, want the composer right under the box", m.inputTop(), got)
	}
}

// TestTheGapAboveTheStatusBarComesFromTheLayout: the bar describes the session,
// not the line being typed, so a reader can put air between them without also
// giving up the row between the conversation and the prompt. Zero is where the
// bar sat before the gap existed.
func TestTheGapAboveTheStatusBarComesFromTheLayout(t *testing.T) {
	for _, gap := range []int{0, 1, 3} {
		m := withLayout(t, func(l *Layout) { l.StatusGap = gap }, testStyles(t), nil)
		// Without a host the bar is empty, and an empty row cannot be told
		// apart from the gap above it.
		m.cfg.Status = func(int) string { return "the session" }
		frame := strings.Split(plain(m.View().Content), "\n")
		if len(frame) != m.height {
			t.Fatalf("statusGap %d: frame has %d rows, want %d", gap, len(frame), m.height)
		}
		// The bar is the last row, and it keeps saying what it said: the gap
		// moves it, it does not replace it.
		if status := frame[m.height-1]; !strings.Contains(status, "the session") {
			t.Fatalf("statusGap %d: last row = %q, want the status bar", gap, status)
		}
		// Exactly the requested number of blank rows sit above it, directly
		// under the prompt: the budget has to come out of the transcript, or
		// the frame would be one row too tall.
		bottom := m.inputTop() + len(m.suggestionLines()) + len(m.composerBlock())
		for row := bottom; row < m.height-1; row++ {
			if got := strings.TrimSpace(frame[row]); got != "" {
				t.Fatalf("statusGap %d: row %d = %q, want blank", gap, row, got)
			}
		}
		if got := m.height - 1 - bottom; got != gap {
			t.Fatalf("statusGap %d drew %d blank rows above the bar", gap, got)
		}
		top := m.inputTop() + m.cfg.Layout.composerTop(m.width)
		if row, _, ok := m.caret(); !ok || row != top {
			t.Fatalf("statusGap %d: caret row = %d, %v, want %d", gap, row, ok, top)
		}
	}
}

// boxWidth reads the width of the one box on screen off the frame itself, which
// is the only place a width means anything: a box is what the terminal gets.
func boxWidth(t *testing.T, m Model) int {
	t.Helper()
	// The composer draws a box too, and this helper is about the overlay one:
	// the rows the input area occupies are skipped by position, so its own top
	// rule is never read as the box's.
	start := m.inputTop() + len(m.suggestionLines())
	end := start + len(m.composerBlock())
	for row, line := range strings.Split(plain(m.View().Content), "\n") {
		if row >= start && row < end {
			continue
		}
		if strings.HasPrefix(line, "╭") {
			return len([]rune(strings.TrimRight(line, " ")))
		}
	}
	t.Fatal("no box on screen")
	return 0
}

// TestTheThinkingRowsComeFromTheLayout: reasoning is a footnote, so it is the
// tail of the stream and never the whole of it. Zero is a reader who wants the
// header and no body.
func TestTheThinkingRowsComeFromTheLayout(t *testing.T) {
	text := "one\ntwo\nthree\nfour\nfive"
	for rows, want := range map[int]int{0: 1, 2: 3, 5: 6} {
		layout := DefaultLayout()
		// One header row plus the tail, which is the count before there were
		// boxes: a framed segment spends two of its rows on rules and puts the
		// header in the top one, and that shape has its own test.
		layout.TranscriptFrames = false
		layout.ThinkingRows = rows
		tr := NewTranscript()
		tr.Append(Block{Role: RoleThinking, Text: text})
		lines := tr.Lines(40, testStyles(t), layout)
		if len(lines) != want {
			t.Fatalf("thinkingRows %d drew %d rows, want %d: %q", rows, len(lines), want, lines)
		}
	}
	// The tail is what survives, and the first row says it was cut.
	layout := DefaultLayout()
	layout.TranscriptFrames = false
	layout.ThinkingRows = 2
	tr := NewTranscript()
	tr.Append(Block{Role: RoleThinking, Text: text})
	lines := tr.Lines(40, testStyles(t), layout)
	if !strings.Contains(lines[1], "… four") || !strings.Contains(lines[2], "five") {
		t.Fatalf("the tail is not what was kept: %q", lines)
	}
}

// TestTheHomePageCanBeTurnedOff is the one setting that is not a number: a
// reader who wants an empty screen above the prompt gets one, and the host's
// facts are simply not asked for.
func TestTheHomePageCanBeTurnedOff(t *testing.T) {
	welcome := func(int) *Welcome { return &Welcome{Version: "9.9.9", Dir: "~"} }
	m := withLayout(t, func(l *Layout) { l.HomePage = false }, testStyles(t), nil)
	m.cfg.Welcome = welcome
	if info := m.welcome(); info != nil {
		t.Fatalf("the page is still drawn: %+v", info)
	}
	if body := plain(m.View().Content); strings.Contains(body, "9.9.9") {
		t.Fatalf("the page is on screen with homePage false:\n%s", body)
	}

	m = withLayout(t, func(l *Layout) { l.HomePage = true }, testStyles(t), nil)
	m.cfg.Welcome = welcome
	if info := m.welcome(); info == nil {
		t.Fatal("homePage true drew no page")
	}
}

// withLayout builds an answered model under a layout a test has changed. The
// transcript is not empty because half of this geometry is only visible next to
// content: a gap on an empty screen is indistinguishable from padding.
func withLayout(t *testing.T, change func(*Layout), styles Styles, suggest func(string) []Suggestion) Model {
	t.Helper()
	layout := DefaultLayout()
	change(&layout)
	m := New(Config{Styles: styles, Layout: layout, Suggest: suggest, Run: func(ctx context.Context, prompt Prompt, sink Sink) error {
		sink.Answer("the answer")
		return nil
	}})
	m = sized(t, m)
	updated, _ := m.Update(typeText("a question"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	for i := 0; i < 8 && m.busy; i++ {
		updated, _ = m.Update(next(t, m))
		m = updated.(Model)
	}
	return m
}

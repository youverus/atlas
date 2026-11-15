package tui

// 0.4.18 boxes a stretch of reasoning and a stretch of calls and titles each
// box with what the stretch was. These are the tests of that shape: that a turn
// alternates box by box, what the title says at each width, and that switching
// the boxes off draws the transcript 0.4.17 drew.

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"pi-go/internal/theme"
)

// boxes reads the row span of each segment box off a rendered transcript: the
// top rule and the closing rule of one box.
func boxes(lines []string) [][2]int {
	var spans [][2]int
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "╭"):
			spans = append(spans, [2]int{index, index})
		case strings.HasPrefix(line, "╰") && len(spans) > 0:
			spans[len(spans)-1][1] = index
		}
	}
	return spans
}

// alternatingTurn is the shape the boxes exist for: a prompt, two stretches of
// reasoning with a stretch of calls between them, and the answer they earned.
func alternatingTurn() Transcript {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleUser, Text: "why is the suite slow"})
	tr.Append(Block{Role: RoleThinking, Text: "It builds every package.", Note: "Thought for 3.1s"})
	tr.Append(Block{Role: RoleTool, Name: "read", Text: `{"path":"run.sh"}`, Note: "42 output lines"})
	tr.Append(Block{Role: RoleTool, Name: "grep", Text: `{"pattern":"go test"}`, Note: "6 matches"})
	tr.Append(Block{Role: RoleThinking, Text: "So it runs twice.", Note: "Thought for 0.4s"})
	tr.Append(Block{Role: RoleTool, Name: "bash", Text: `{"cmd":"go test ./..."}`, Note: "1 output line"})
	tr.Append(Block{Role: RoleAnswer, Text: "The gate runs it once and you ran it once."})
	return tr
}

// TestATurnReadsAsAlternatingSegments: four stretches, four boxes, in the order
// they happened, each one exactly as wide as the screen and closed at both
// ends.
func TestATurnReadsAsAlternatingSegments(t *testing.T) {
	const width = 80
	tr := alternatingTurn()
	lines := tr.Lines(width, testStyles(t), DefaultLayout())
	body := strings.Join(lines, "\n")

	spans := boxes(lines)
	if len(spans) != 4 {
		t.Fatalf("got %d boxes, want four (two stretches of reasoning, two of calls):\n%s", len(spans), body)
	}
	for _, want := range []string{"Thought for 3.1s", "2 calls · read, grep", "Thought for 0.4s", "1 call · bash"} {
		found := false
		for _, span := range spans {
			if strings.Contains(lines[span[0]], want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no box is titled %q:\n%s", want, body)
		}
	}
	for index, span := range spans {
		if span[0] >= span[1] {
			t.Fatalf("box %d is never closed: %q", index, lines[span[0]])
		}
		// Every row of the box is the screen's width: a rule that stops short
		// leaves the box open on one side.
		for row := span[0]; row <= span[1]; row++ {
			if got := lipgloss.Width(lines[row]); got != width {
				t.Fatalf("row %d of box %d is %d cells wide, want %d: %q", row, index, got, width, lines[row])
			}
			if row > span[0] && row < span[1] && !strings.HasPrefix(lines[row], "│") {
				t.Fatalf("row %d of box %d has no left border: %q", row, index, lines[row])
			}
		}
		// Air around the box, and none inside it.
		if span[0] > 0 && lines[span[0]-1] != "" {
			t.Fatalf("box %d starts on row %d under %q, want a blank row", index, span[0], lines[span[0]-1])
		}
		if span[1]+1 < len(lines) && lines[span[1]+1] != "" {
			t.Fatalf("box %d ends on row %d above %q, want a blank row", index, span[1], lines[span[1]+1])
		}
	}
	// The two calls share one box: the stretch is the unit, not the call.
	calls := spans[1]
	if !strings.Contains(lines[calls[0]+1], "read ") || !strings.Contains(lines[calls[0]+2], "grep ") {
		t.Fatalf("the two calls are not one box:\n%s", body)
	}
	// The answer is not a segment: it keeps its own bar and no rule.
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, AnswerBar) || strings.Contains(last, "╭") {
		t.Fatalf("answer row = %q, want the answer's own bar and no box", last)
	}
}

// TestASingleCallIsStillABox: one call is a stretch of one, because the box is
// what tells the reader where the stretch ended and a single call ends too.
func TestASingleCallIsStillABox(t *testing.T) {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleThinking, Text: "One read will do.", Note: "Thought for 0.2s"})
	tr.Append(Block{Role: RoleTool, Name: "read", Text: `{"path":"x"}`, Note: "3 lines"})

	lines := tr.Lines(60, testStyles(t), DefaultLayout())
	spans := boxes(lines)
	if len(spans) != 2 {
		t.Fatalf("got %d boxes, want one per stretch:\n%s", len(spans), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[spans[1][0]], "1 call · read") {
		t.Fatalf("the single call's box is titled %q", lines[spans[1][0]])
	}
	if len(lines) != 7 {
		t.Fatalf("two boxes of one body row each drew %d rows, want 7: %q", len(lines), lines)
	}
}

// TestAnOpenThinkingSegmentSaysItIsThinking: while the stream is arriving there
// is no duration to report, so the title says what the box is instead.
func TestAnOpenThinkingSegmentSaysItIsThinking(t *testing.T) {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleThinking, Text: "half a thought", Open: true})

	lines := tr.Lines(60, testStyles(t), DefaultLayout())
	spans := boxes(lines)
	if len(spans) != 1 || !strings.Contains(lines[spans[0][0]], "thinking") {
		t.Fatalf("an open stretch is titled %q, want thinking", lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "half a thought") {
		t.Fatalf("the box lost the text that had arrived: %q", lines)
	}
}

// TestACallBoxNamesAtMostThreeTools: the title is a summary, not an inventory,
// so a long stretch names the first three and counts the rest.
func TestACallBoxNamesAtMostThreeTools(t *testing.T) {
	tr := NewTranscript()
	for _, name := range []string{"read", "grep", "glob", "edit", "bash"} {
		tr.Append(Block{Role: RoleTool, Name: name, Note: "ok"})
	}
	lines := tr.Lines(80, testStyles(t), DefaultLayout())
	spans := boxes(lines)
	if len(spans) != 1 {
		t.Fatalf("five calls drew %d boxes, want one:\n%s", len(spans), strings.Join(lines, "\n"))
	}
	if want := "5 calls · read, grep, glob, +2"; !strings.Contains(lines[spans[0][0]], want) {
		t.Fatalf("title = %q, want %q", lines[spans[0][0]], want)
	}
}

// TestAFailedCallKeepsItsBodyInsideItsBox: the body of a failure is the one
// result a reader has to act on, and it sits under the call it belongs to.
func TestAFailedCallKeepsItsBodyInsideItsBox(t *testing.T) {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleTool, Name: "bash", Note: "exit 1", Detail: "no such file\nsecond line"})

	body := strings.Join(tr.Lines(60, testStyles(t), DefaultLayout()), "\n")
	if strings.Contains(body, depthMarker) {
		t.Fatalf("the box still carries the branch glyph:\n%s", body)
	}
	for _, want := range []string{"\n│   no such file", "\n│   second line"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the failure body is missing %q:\n%s", want, body)
		}
	}
}

// TestANarrowScreenDropsTheDescriptionBeforeTheBox: the title is worth less
// than the structure it labels, so it shortens step by step and only then does
// the stretch fall back to one line per block.
func TestANarrowScreenDropsTheDescriptionBeforeTheBox(t *testing.T) {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleTool, Name: "read", Note: "ok"})
	tr.Append(Block{Role: RoleTool, Name: "grep", Note: "ok"})

	lines := tr.Lines(20, testStyles(t), DefaultLayout())
	spans := boxes(lines)
	if len(spans) != 1 {
		t.Fatalf("20 columns dropped the box:\n%s", strings.Join(lines, "\n"))
	}
	title := lines[spans[0][0]]
	if !strings.Contains(title, "2 calls · read") || strings.Contains(title, "grep") {
		t.Fatalf("the title did not shorten to fit: %q", title)
	}

	lines = tr.Lines(10, testStyles(t), DefaultLayout())
	if spans := boxes(lines); len(spans) != 0 {
		t.Fatalf("10 columns kept a box that cannot hold its title:\n%s", strings.Join(lines, "\n"))
	}
	if got := strings.Join(lines, "\n"); !strings.Contains(got, depthMarker) {
		t.Fatalf("the fallback lost the branch glyph:\n%s", got)
	}
}

// TestTheThinkingTailIsCutInsideTheBox: the tail is the rule the block already
// had, and the mark that says it was cut is still on the first body row — one
// row lower than it used to be, because the title took the row the header had.
func TestTheThinkingTailIsCutInsideTheBox(t *testing.T) {
	layout := DefaultLayout()
	layout.ThinkingRows = 2
	tr := NewTranscript()
	tr.Append(Block{Role: RoleThinking, Text: "one\ntwo\nthree\nfour\nfive"})

	lines := tr.Lines(40, testStyles(t), layout)
	if len(lines) != 4 {
		t.Fatalf("two body rows in a box drew %d rows, want a rule, two rows and a rule: %q", len(lines), lines)
	}
	if !strings.Contains(lines[1], "… four") || !strings.Contains(lines[2], "five") {
		t.Fatalf("the tail is not what was kept: %q", lines)
	}
}

// TestTheSegmentBoxIsPaintedEdgeToEdge: a box whose middle is the terminal's
// own color is a stripe with a rule around it, so every cell of every row it
// draws has to carry the screen's background.
func TestTheSegmentBoxIsPaintedEdgeToEdge(t *testing.T) {
	library, err := theme.NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	screen := painted(t, "48", resolved.Palette.Background.Hex())

	tr := NewTranscript()
	tr.Append(Block{Role: RoleThinking, Text: "a thought", Note: "Thought for 0.1s"})
	tr.Append(Block{Role: RoleTool, Name: "read", Note: "ok"})

	const width = 60
	lines := tr.Lines(width, NewStyles(resolved), DefaultLayout())
	// The rules are found on the plain text: a painted row leads with its
	// escape, and the cells are read on the painted row below.
	stripped := make([]string, len(lines))
	for index, line := range lines {
		stripped[index] = plain(line)
	}
	spans := boxes(stripped)
	if len(spans) != 2 {
		t.Fatalf("got %d boxes, want two", len(spans))
	}
	for _, span := range spans {
		for row := span[0]; row <= span[1]; row++ {
			cells := cellBackgrounds(lines[row])
			if len(cells) != width {
				t.Fatalf("row %d has %d cells, want %d: %q", row, len(cells), width, lines[row])
			}
			for column, got := range cells {
				if got != screen {
					t.Fatalf("row %d column %d carries background %q, want %q: %q",
						row, column, got, screen, lines[row])
				}
			}
		}
	}
}

// TestTurningTheFramesOffDrawsTheOldTranscript: the switch is the whole of the
// difference, so with it off the transcript is the one 0.4.17 drew — no rule,
// the branch glyph back, and the blank line where prose meets calls.
func TestTurningTheFramesOffDrawsTheOldTranscript(t *testing.T) {
	layout := DefaultLayout()
	layout.TranscriptFrames = false
	tr := alternatingTurn()
	lines := tr.Lines(80, testStyles(t), layout)

	if spans := boxes(lines); len(spans) != 0 {
		t.Fatalf("frames off still drew boxes:\n%s", strings.Join(lines, "\n"))
	}
	if len(lines) != 14 {
		t.Fatalf("frames off drew %d rows, want 14: %q", len(lines), lines)
	}
	// A prompt, air, the reasoning header, the reasoning, air, the two calls.
	if lines[0] != "> why is the suite slow" || lines[1] != "" {
		t.Fatalf("rows 0 and 1 = %q, %q, want the prompt and air", lines[0], lines[1])
	}
	if !strings.Contains(lines[2], "… Thought for 3.1s") || lines[3] != "    It builds every package." {
		t.Fatalf("rows 2 and 3 = %q, %q, want the reasoning where 0.4.17 put it", lines[2], lines[3])
	}
	if lines[4] != "" {
		t.Fatalf("row 4 = %q, want the blank line before the calls", lines[4])
	}
	if !strings.Contains(lines[5], depthMarker+"read") || !strings.Contains(lines[6], depthMarker+"grep") {
		t.Fatalf("rows 5 and 6 = %q, %q, want two calls under the branch glyph", lines[5], lines[6])
	}
}

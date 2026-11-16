package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// replayed is a model that has just eaten a resumed session: total closed
// answers, arriving as the burst writeTranscript produces.
func replayed(t *testing.T, total int) Model {
	t.Helper()
	m := sized(t, New(Config{Styles: testStyles(t)}))
	for i := 0; i < total; i++ {
		updated, _ := m.Update(AnswerMsg{Text: fmt.Sprintf("answer %d", i)})
		m = updated.(Model)
	}
	return m
}

// plainRows strips the styling from a window, for assertions that compare
// which rows are on screen rather than how they are painted.
func plainRows(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = plain(line)
	}
	return out
}

func TestTranscriptFrameStartsAtTheRevealWindow(t *testing.T) {
	tr := NewTranscript()
	for i := 0; i < 100; i++ {
		tr.Append(Block{Role: RoleAnswer, Text: fmt.Sprintf("line %d", i)})
	}
	tr.Trim(replayWindowSpans)
	if hidden := tr.Hidden(); hidden != 100-replayWindowSpans {
		t.Fatalf("hidden = %d, want %d", hidden, 100-replayWindowSpans)
	}
	frame := tr.Frame(80, testStyles(t), DefaultLayout())
	if frame.FirstSpan != tr.Hidden() {
		t.Fatalf("FirstSpan = %d, want the reveal span %d", frame.FirstSpan, tr.Hidden())
	}
	if len(frame.Lines) != replayWindowSpans {
		t.Fatalf("got %d lines, want the %d-span window:\n%s",
			len(frame.Lines), replayWindowSpans, strings.Join(frame.Lines, "\n"))
	}
	if !strings.Contains(frame.Lines[0], "line 36") {
		t.Fatalf("window starts at %q, want the first kept line", frame.Lines[0])
	}
	// Clicks and folds speak transcript span indices whatever part of the
	// conversation the window starts at.
	index, ok := frame.SpanAt(0)
	if !ok || index != frame.FirstSpan {
		t.Fatalf("SpanAt(0) = %d, %v; want the transcript index %d", index, ok, frame.FirstSpan)
	}
	if rows, ok := frame.RowsFor(frame.FirstSpan); !ok || rows.First != 0 {
		t.Fatalf("RowsFor(%d) = %+v, %v; want the first row", frame.FirstSpan, rows, ok)
	}
	// Reveal moves the top of the window back and keeps the tail.
	tr.Reveal(revealChunkSpans)
	frame = tr.Frame(80, testStyles(t), DefaultLayout())
	if frame.FirstSpan != 100-replayWindowSpans-revealChunkSpans {
		t.Fatalf("FirstSpan = %d, want %d", frame.FirstSpan, 100-replayWindowSpans-revealChunkSpans)
	}
	if len(frame.Lines) != replayWindowSpans+revealChunkSpans {
		t.Fatalf("got %d lines, want %d", len(frame.Lines), replayWindowSpans+revealChunkSpans)
	}
	if !strings.Contains(frame.Lines[0], "line 4") {
		t.Fatalf("window starts at %q, want the newly revealed line", frame.Lines[0])
	}
	if !strings.Contains(frame.Lines[len(frame.Lines)-1], "line 99") {
		t.Fatalf("window ends at %q, want the newest line", frame.Lines[len(frame.Lines)-1])
	}
}

func TestModelResumeKeepsOnlyTheRecentWindow(t *testing.T) {
	const total = 200
	m := replayed(t, total)
	tr := m.views[ViewMain].transcript
	if hidden := tr.Hidden(); hidden != total-replayWindowSpans {
		t.Fatalf("hidden = %d, want %d", hidden, total-replayWindowSpans)
	}
	lines := m.transcriptLines(m.active, m.width)
	if len(lines) != replayWindowSpans {
		t.Fatalf("got %d lines, want the %d-span window", len(lines), replayWindowSpans)
	}
	if !strings.Contains(lines[0], fmt.Sprintf("answer %d", total-replayWindowSpans)) {
		t.Fatalf("window starts at %q, want the first kept answer", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "answer 199") {
		t.Fatalf("window ends at %q, want the newest answer", lines[len(lines)-1])
	}
	// The newest answer sits against the composer: the window is bottom
	// anchored, so the reader meets the conversation where they left it.
	if m.activeView().offset != 0 {
		t.Fatalf("offset = %d, want 0", m.activeView().offset)
	}
}

func TestModelRevealsEarlierMessagesWhileScrollingAtTheTop(t *testing.T) {
	const total = 200
	m := replayed(t, total)
	// Jump to the top of the rendered window in one move: nothing reveals on
	// the way, because the reader was not at the top when it began.
	m.scrollBy(m.maxOffset())
	if hidden := m.views[ViewMain].transcript.Hidden(); hidden != total-replayWindowSpans {
		t.Fatalf("hidden = %d, want %d before any reveal", hidden, total-replayWindowSpans)
	}
	// Scrolling up at the top reveals a chunk at a time. Every step must move
	// exactly one new row in: the reveal prepends rows above the window, and a
	// jump would break the row-after-row continuity below. The styling is
	// stripped before comparing: a zero selection paints the first cell of
	// row 0 reversed — a pre-existing quirk, not a window change — and the
	// assertion is about which rows are on screen.
	body := m.bodyHeight()
	for m.views[ViewMain].transcript.Hidden() > 0 {
		before := m.bodyLines()
		m.scrollBy(1)
		after := m.bodyLines()
		if len(before) != body || len(after) != body {
			t.Fatalf("window height %d/%d, want %d", len(before), len(after), body)
		}
		if !slices.Equal(plainRows(after[1:]), plainRows(before[:body-1])) {
			t.Fatalf("scroll at the top jumped the window:\nbefore[0] = %q\nafter[1]  = %q",
				plain(before[0]), plain(after[1]))
		}
	}
	// The whole conversation is reachable, and its first line is.
	for m.activeView().offset < m.maxOffset() {
		m.scrollBy(1)
	}
	if top := m.bodyLines()[0]; !strings.Contains(top, "answer 0") {
		t.Fatalf("top of the conversation = %q, want the first answer", top)
	}
}

func TestModelCtrlHomeRevealsAChunk(t *testing.T) {
	const total = 200
	m := replayed(t, total)
	updated, _ := m.Update(press(tea.KeyHome, tea.ModCtrl))
	m = updated.(Model)
	if hidden := m.views[ViewMain].transcript.Hidden(); hidden != total-replayWindowSpans-revealChunkSpans {
		t.Fatalf("hidden = %d, want %d", hidden, total-replayWindowSpans-revealChunkSpans)
	}
	if offset := m.activeView().offset; offset != m.maxOffset() {
		t.Fatalf("offset = %d, want the top %d", offset, m.maxOffset())
	}
	if top := m.bodyLines()[0]; !strings.Contains(top, fmt.Sprintf("answer %d", total-replayWindowSpans-revealChunkSpans)) {
		t.Fatalf("top row = %q, want the first revealed answer", top)
	}
}

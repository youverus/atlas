package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"pi-go/internal/theme"
	"pi-go/internal/wire"
)

// testStyles is the zero value: a lipgloss style with no rules renders its
// input unchanged, so an assertion can read the frame without stripping escape
// codes. Colour is a separate test.
func testStyles(t *testing.T) Styles {
	t.Helper()
	return Styles{}
}

func resolvedStyles(t *testing.T) Styles {
	t.Helper()
	library, err := theme.NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return NewStyles(resolved)
}

func TestNewStylesSpeaksHex(t *testing.T) {
	library, err := theme.NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	styles := NewStyles(resolved)
	rendered := styles.Accent.Render("x")
	want := resolved.Palette.Accent.Hex()
	if want == "" {
		t.Skip("the dark default leaves the accent to the terminal")
	}
	r, g, b := hexTriplet(t, want)
	// Only the numbers: lipgloss writes the foreground and the background of a
	// cell into one escape, so the exact byte string depends on what else the
	// style paints. The color this test is about is the one after "38;2;".
	if painted := fmt.Sprintf("38;2;%d;%d;%d", r, g, b); !strings.Contains(rendered, painted) {
		t.Fatalf("accent did not paint %s: %q", want, rendered)
	}
	// And the screen's own color rides along, because a cell is painted whole:
	// a foreground alone leaves the terminal's background showing through the
	// letter's cell, which is the seam that made a full-screen background
	// impossible before 0.4.15.
	if background := resolved.Palette.Background.Hex(); background != "" {
		br, bg, bb := hexTriplet(t, background)
		if painted := fmt.Sprintf("48;2;%d;%d;%d", br, bg, bb); !strings.Contains(rendered, painted) {
			t.Fatalf("accent did not carry the screen color %s: %q", background, rendered)
		}
	}
}

func hexTriplet(t *testing.T, color string) (int, int, int) {
	t.Helper()
	if len(color) != 7 {
		t.Fatalf("color %q is not #rrggbb", color)
	}
	parse := func(s string) int {
		var value int
		if _, err := fmt.Sscanf(s, "%02x", &value); err != nil {
			t.Fatalf("color %q: %v", color, err)
		}
		return value
	}
	return parse(color[1:3]), parse(color[3:5]), parse(color[5:7])
}

// sgr is what a styled row carries. The zero Styles still bolds the picked row
// of a box, so a test that reads a frame has to look at what the terminal would
// show rather than at the bytes.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(text string) string { return sgr.ReplaceAllString(text, "") }

func press(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: mod})
}

func typeText(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: []rune(text)[0], Text: text})
}

// next waits for the next message the turn posts. It reads the host channel
// rather than running the reader command: the reader is a property of the
// program's loop, and a test that ran its own copy would be the second reader
// the interface must never have.
func next(t *testing.T, m Model) tea.Msg {
	t.Helper()
	select {
	case msg := <-m.host.events:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a message")
		return nil
	}
}

func sized(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model)
}

func TestTranscriptFoldsAnAnswer(t *testing.T) {
	tr := NewTranscript()
	fixed := time.Unix(1000, 0)
	tr.Now = func() time.Time { return fixed }

	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
		Type: wire.EventThinkingStart,
	}})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
		Type:  wire.EventThinkingDelta,
		Delta: "weighing options",
	}})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
		Type: wire.EventThinkingEnd,
	}})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
		Type: wire.EventTextDelta, Delta: "first",
	}})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
		Type: wire.EventTextDelta, Delta: " second",
	}})

	blocks := tr.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Role != RoleThinking || blocks[0].Text != "weighing options" {
		t.Fatalf("thinking block = %+v", blocks[0])
	}
	if blocks[0].Open {
		t.Fatal("thinking block is still open after thinking_end")
	}
	if blocks[0].Note != "Thought for 0ms" {
		t.Fatalf("thinking note = %q, want a duration", blocks[0].Note)
	}
	if blocks[1].Role != RoleAnswer || blocks[1].Text != "first second" {
		t.Fatalf("answer block = %+v", blocks[1])
	}
	if !blocks[1].Open {
		t.Fatal("answer block closed before the message ended")
	}
}

func TestTranscriptPairsToolCallsByID(t *testing.T) {
	tr := NewTranscript()
	failed := true
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: "a", ToolName: "read"})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: "b", ToolName: "bash", Args: []byte(`{"command":"ls -la"}`)})
	// Results come back out of order when calls run in parallel.
	tr.Apply(wire.AgentEvent{
		Type:       wire.AgentEventToolExecEnd,
		ToolCallID: "b",
		ToolName:   "bash",
		IsError:    &failed,
		Result:     []byte(`{"content":[{"type":"text","text":"boom\n"}]}`),
	})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecEnd, ToolCallID: "a", ToolName: "read"})

	blocks := tr.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Name != "read" || blocks[0].Note != "no output" || blocks[0].Detail != "" {
		t.Fatalf("read block = %+v", blocks[0])
	}
	if blocks[1].Name != "bash" || !strings.Contains(blocks[1].Text, "ls -la") {
		t.Fatalf("bash block = %+v", blocks[1])
	}
	if blocks[1].Note != "failed" || blocks[1].Detail != "boom" {
		t.Fatalf("failed bash note/detail = %q / %q", blocks[1].Note, blocks[1].Detail)
	}
	for _, block := range blocks {
		if block.Open {
			t.Fatalf("%s still open", block.Name)
		}
	}
}

func TestTranscriptClosesARunningToolWhenAnotherStarts(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: "a", ToolName: "read"})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: "b", ToolName: "bash"})

	blocks := tr.Blocks()
	if !strings.Contains(blocks[0].Note, "running") {
		t.Fatalf("first block note = %q, want a running marker", blocks[0].Note)
	}
	if !blocks[1].Open {
		t.Fatal("second block should be the open one")
	}
}

func TestTranscriptDoesNotPaintAStopAsAnError(t *testing.T) {
	// A provider words its own cancellation ("openai: request aborted") and puts
	// it in ErrorMessage like any failure. Painting that in red tells a reader
	// who pressed the key that the model broke.
	tr := NewTranscript()
	aborted := wire.Message{
		Role:         wire.RoleAssistant,
		Content:      wire.NewBlocks(),
		StopReason:   wire.StopAborted,
		ErrorMessage: "openai: request aborted",
	}
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageStart, Message: &aborted})
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageEnd, Message: &aborted})

	for _, block := range tr.Blocks() {
		if block.Role == RoleError {
			t.Fatalf("an aborted turn was painted as %q", block.Text)
		}
	}
}

func TestTranscriptCallsAStoppedToolInterrupted(t *testing.T) {
	// The kernel reports a call cut short by a cancellation as an error result
	// carrying its own words. The reader stopped the turn; nothing failed.
	tr := NewTranscript()
	failed := true
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: "a", ToolName: "bash"})
	tr.Stop()
	tr.Apply(wire.AgentEvent{
		Type:       wire.AgentEventToolExecEnd,
		ToolCallID: "a",
		ToolName:   "bash",
		IsError:    &failed,
		Result:     []byte(`{"content":[{"type":"text","text":"Operation aborted"}]}`),
	})

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].Note != "interrupted" || blocks[0].Detail != "" {
		t.Fatalf("note/detail = %q / %q, want interrupted with no body", blocks[0].Note, blocks[0].Detail)
	}
	// The word lives for one turn: the next one reports its own failures.
	tr.Finish()
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: "b", ToolName: "bash"})
	tr.Apply(wire.AgentEvent{
		Type:       wire.AgentEventToolExecEnd,
		ToolCallID: "b",
		ToolName:   "bash",
		IsError:    &failed,
		Result:     []byte(`{"content":[{"type":"text","text":"boom"}]}`),
	})
	if note := tr.Blocks()[1].Note; note != "failed" {
		t.Fatalf("next turn note = %q, want failed", note)
	}
}

func TestTranscriptDoesNotRepeatAStreamedAnswer(t *testing.T) {
	// The provider reports every answer twice: as deltas, then whole on the
	// message that ends it. Folding both put each answer on screen twice.
	tr := NewTranscript()
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageStart, Message: &wire.Message{Role: wire.RoleAssistant}})
	for _, delta := range []string{"the ", "answer"} {
		tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
			Type: wire.EventTextDelta, Delta: delta,
		}})
	}
	end := wire.AssistantMessage(wire.TextContent("the answer"))
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageEnd, Message: &end})

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	if blocks[0].Role != RoleAnswer || blocks[0].Text != "the answer" {
		t.Fatalf("answer block = %+v", blocks[0])
	}
	if blocks[0].Open {
		t.Fatal("answer block is still open after message_end")
	}
}

func TestTranscriptKeepsAnAnswerThatWasNotStreamed(t *testing.T) {
	// The other half of the same rule: a provider that streams no deltas still
	// has the answer, and dropping it leaves the prompt with nothing under it.
	tr := NewTranscript()
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageStart, Message: &wire.Message{Role: wire.RoleAssistant}})
	end := wire.AssistantMessage(wire.TextContent("stated once"))
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventMessageEnd, Message: &end})

	blocks := tr.Blocks()
	if len(blocks) != 1 || blocks[0].Text != "stated once" {
		t.Fatalf("blocks = %+v, want one answer", blocks)
	}
	if blocks[0].Open {
		t.Fatal("answer block is still open after message_end")
	}
}

func TestTranscriptLinesMarkAndSeparate(t *testing.T) {
	tr := NewTranscript()
	tr.Append(Block{Role: RoleUser, Text: "hello"})
	tr.Append(Block{Role: RoleTool, Name: "read", Note: "3 output lines"})
	tr.Append(Block{Role: RoleTool, Name: "grep", Note: "no output"})
	tr.Append(Block{Role: RoleAnswer, Text: "done"})

	// The frames are off: these assertions are about where the blank line goes
	// and what the marker says, and a segment's rule answers the first one
	// differently. The boxed transcript is read in transcript_frames_test.go.
	layout := DefaultLayout()
	layout.TranscriptFrames = false
	lines := tr.Lines(40, testStyles(t), layout)
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[0], userMarker) {
		t.Fatalf("user line = %q", lines[0])
	}
	// A blank line goes between prose and the tree, and between the tree and the
	// answer, but not between two calls.
	if lines[1] != "" {
		t.Fatalf("line 1 = %q, want the separator before the tree", lines[1])
	}
	if !strings.Contains(lines[2], "read") || !strings.Contains(lines[2], "3 output lines") {
		t.Fatalf("first tool line = %q", lines[2])
	}
	if lines[3] == "" {
		t.Fatal("two consecutive calls should not be separated")
	}
	if lines[4] != "" {
		t.Fatalf("line 4 = %q, want the separator after the tree", lines[4])
	}
	if !strings.HasPrefix(lines[5], AnswerBar) {
		t.Fatalf("answer line = %q, want the bar", lines[5])
	}
}

func TestTranscriptRunsTheBarDownEveryAnswerLine(t *testing.T) {
	tr := NewTranscript()
	// An answer that wraps: the bar has to be on every line of it, not only the
	// first, or the block stops reading as one answer halfway down the screen.
	tr.Append(Block{Role: RoleAnswer, Text: strings.TrimSpace(strings.Repeat("word ", 40))})

	lines := tr.Lines(20, testStyles(t), DefaultLayout())
	if len(lines) < 3 {
		t.Fatalf("the answer did not wrap: %v", lines)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, AnswerBar) {
			t.Fatalf("line %d = %q, want the bar", i, line)
		}
		if cells := len([]rune(line)); cells > 20 {
			t.Fatalf("line %d is %d cells wide, want at most 20: %q", i, cells, line)
		}
	}
}

func TestTranscriptSeparatesThePromptFromBothSides(t *testing.T) {
	// The prompt is the one block the reader did not get from the model, and it
	// is what a reader scrolls back to find. With no air around it, a question
	// sat on the answer above it and read as part of that answer.
	tr := NewTranscript()
	tr.Append(Block{Role: RoleAnswer, Text: "first answer"})
	tr.Append(Block{Role: RoleUser, Text: "a question"})
	tr.Append(Block{Role: RoleAnswer, Text: "second answer"})

	want := []string{AnswerBar + "first answer", "", userMarker + "a question", "", AnswerBar + "second answer"}
	got := tr.Lines(40, testStyles(t), DefaultLayout())
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestTranscriptPaintsThePromptInItsOwnColour(t *testing.T) {
	library, err := theme.NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	user, text := resolved.Palette.User.Hex(), resolved.Palette.Text.Hex()
	if user == "" || text == "" || user == text {
		t.Skip("this theme leaves the prompt to the terminal or paints it like an answer")
	}

	tr := NewTranscript()
	tr.Append(Block{Role: RoleUser, Text: "hello"})
	line := tr.Lines(40, NewStyles(resolved), DefaultLayout())[0]
	// Both halves of the line: colouring only the marker left the words beside
	// it painted like an answer, which is the half a reader actually reads.
	r, g, b := hexTriplet(t, user)
	if want := fmt.Sprintf("38;2;%d;%d;%d", r, g, b); !strings.Contains(line, want) {
		t.Fatalf("the prompt is not painted %s: %q", user, line)
	}
	r, g, b = hexTriplet(t, text)
	if unwanted := fmt.Sprintf("38;2;%d;%d;%d", r, g, b); strings.Contains(line, unwanted) {
		t.Fatalf("the prompt is painted in the answer's colour: %q", line)
	}
}

func TestComposerWrapsAndPlacesTheCursor(t *testing.T) {
	var c Composer
	c.Set("abcdefgh")
	styles := testStyles(t)
	lines := c.Lines("> ", 9, styles.Accent, styles.Text, "")
	// Two columns go to the prompt, so seven cells fit and the eighth wraps.
	if len(lines) != 2 {
		t.Fatalf("got %d rows, want 2:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	row, col := c.Cursor("> ", 9)
	if row != 1 || col != 3 {
		t.Fatalf("cursor = (%d,%d), want (1,3)", row, col)
	}
	c.Home()
	row, col = c.Cursor("> ", 9)
	if row != 0 || col != 2 {
		t.Fatalf("home cursor = (%d,%d), want (0,2)", row, col)
	}
}

func TestComposerCountsWideCharacters(t *testing.T) {
	var c Composer
	c.Set("中文")
	// Two double-width characters occupy four cells, so the caret sits at the
	// prompt plus four, not plus two.
	_, col := c.Cursor("> ", 20)
	if col != 2+4 {
		t.Fatalf("cursor column = %d, want 6", col)
	}
}

func TestComposerHistoryKeepsTheDraft(t *testing.T) {
	var c Composer
	c.Set("first")
	if _, ok := c.Take(); !ok {
		t.Fatal("first line was not submitted")
	}
	c.Set("second")
	if _, ok := c.Take(); !ok {
		t.Fatal("second line was not submitted")
	}
	c.Set("half typed")
	if !c.Previous() || c.String() != "second" {
		t.Fatalf("previous = %q, want second", c.String())
	}
	if !c.Previous() || c.String() != "first" {
		t.Fatalf("previous = %q, want first", c.String())
	}
	if c.Previous() {
		t.Fatal("previous walked past the oldest entry")
	}
	if !c.Next() || c.String() != "second" {
		t.Fatalf("next = %q, want second", c.String())
	}
	if !c.Next() || c.String() != "half typed" {
		t.Fatalf("next = %q, want the draft back", c.String())
	}
}

func TestComposerInsertsPastedNewlinesAsSpaces(t *testing.T) {
	var c Composer
	c.Insert("one\ntwo")
	if c.String() != "one two" {
		t.Fatalf("pasted = %q", c.String())
	}
}

func TestModelSubmitsAndFoldsEvents(t *testing.T) {
	var got string
	done := make(chan struct{})
	runner := func(ctx context.Context, line string, sink Sink) error {
		got = line
		sink.Event(wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{
			Type: wire.EventTextDelta, Delta: "hi there",
		}})
		close(done)
		return nil
	}

	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("hello"))
	m = updated.(Model)

	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	if !m.busy {
		t.Fatal("model should be busy after submitting")
	}
	if blocks := m.views[ViewMain].transcript.Blocks(); len(blocks) != 1 || blocks[0].Role != RoleUser {
		t.Fatalf("transcript after submit = %+v", blocks)
	}

	// The first message is the answer delta; the second closes the turn.
	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if blocks := m.views[ViewMain].transcript.Blocks(); len(blocks) != 2 || blocks[1].Text != "hi there" {
		t.Fatalf("transcript after delta = %+v", blocks)
	}

	<-done
	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if m.busy {
		t.Fatal("model still busy after the turn ended")
	}
	if !m.host.armed {
		// The listener outlives the turn: notes arrive between turns too.
		t.Fatal("the model stopped listening after the turn ended")
	}
	if got != "hello" {
		t.Fatalf("runner saw %q", got)
	}
	if blocks := m.views[ViewMain].transcript.Blocks(); blocks[1].Open {
		t.Fatal("answer block left open after the turn")
	}
}

func TestModelAsksAndAnswers(t *testing.T) {
	approved := make(chan AskAnswer, 1)
	runner := func(ctx context.Context, line string, sink Sink) error {
		approved <- sink.Ask(AskRequest{Subject: "Ran  bash  ls", Tool: "bash"})
		return nil
	}

	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if m.approval == nil {
		t.Fatal("no modal after the gate asked")
	}
	content := m.View().Content
	if !strings.Contains(content, "allow") || !strings.Contains(content, "Ran  bash  ls") {
		t.Fatalf("modal not drawn:\n%s", content)
	}
	if m.View().Cursor != nil {
		t.Fatal("the caret should be hidden while the modal is up")
	}

	// A key that is neither yes nor no leaves the question where it was.
	updated, _ = m.Update(typeText("x"))
	m = updated.(Model)
	if m.approval == nil {
		t.Fatal("an unrelated key dismissed the modal")
	}

	updated, _ = m.Update(typeText("y"))
	m = updated.(Model)
	if m.approval != nil {
		t.Fatal("the modal stayed up after answering")
	}
	if got := <-approved; !got.Allowed || got.Always {
		t.Fatalf("answer = %+v, want one yes and no standing permission", got)
	}
	next(t, m) // the turn ends; nothing here depends on its message.
}

func TestModelSecondQuestionIsDenied(t *testing.T) {
	first := make(chan AskAnswer, 1)
	second := make(chan AskAnswer, 1)
	runner := func(ctx context.Context, line string, sink Sink) error {
		first <- sink.Ask(AskRequest{Subject: "one", Tool: "bash"})
		second <- sink.Ask(AskRequest{Subject: "two", Tool: "bash"})
		return nil
	}

	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	// Answering no lets the turn ask again; the second question arrives while
	// no modal is up, so it is the one that gets shown.
	updated, _ = m.Update(typeText("n"))
	m = updated.(Model)
	<-first

	// Answering queued a repaint behind the question, and both travel the same
	// channel, so read until the modal is up again.
	for i := 0; i < 8 && m.approval == nil; i++ {
		updated, _ = m.Update(next(t, m))
		m = updated.(Model)
	}
	if m.approval == nil {
		t.Fatal("the second question was not shown")
	}
	updated, _ = m.Update(typeText("n"))
	m = updated.(Model)
	if got := <-second; got.Allowed {
		t.Fatalf("the second question recorded a yes: %+v", got)
	}
}

// running starts a turn that only ends when its context does, and returns the
// model with that turn in flight.
func running(t *testing.T, cfg Config) Model {
	t.Helper()
	blocked := make(chan struct{})
	run := cfg.Run
	cfg.Run = func(ctx context.Context, line string, sink Sink) error {
		if run != nil {
			return run(ctx, line, sink)
		}
		close(blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	m := sized(t, New(cfg))
	updated, _ := m.Update(typeText("think"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	if run == nil {
		<-blocked
	}
	return m
}

func TestTheStopGestureTakesTwoPresses(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t)})

	// One press is a slip: the turn keeps running and the screen says what the
	// second press would do.
	updated, _ := m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	if m.cancel == nil || !m.busy {
		t.Fatal("one press stopped the turn")
	}
	if !m.interruptArmed {
		t.Fatal("the first press did not arm the stop")
	}
	if got, want := m.placeholder(), "esc again to interrupt"; got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}

	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	if m.cancel != nil {
		t.Fatal("cancel was not released")
	}
	if m.interruptArmed {
		t.Fatal("the stop stayed armed after it was spent")
	}

	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if m.busy {
		t.Fatal("still busy after the cancellation came back")
	}
	blocks := m.views[ViewMain].transcript.Blocks()
	if last := blocks[len(blocks)-1]; last.Role != RoleNotice || last.Text != "interrupted" {
		t.Fatalf("last block = %+v, want an interrupted notice", last)
	}
}

func TestTheStopGestureIsDisarmedByAnotherKey(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t), Suggest: palette})
	updated, _ := m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)

	updated, _ = m.Update(typeText("/m"))
	m = updated.(Model)
	if m.interruptArmed {
		t.Fatal("typing left the stop armed")
	}

	// The turn is still there, and so is what was typed: a key that is not a
	// stop key is the reader saying they are still working.
	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	if m.cancel == nil || !m.busy {
		t.Fatal("a disarm-then-press sequence stopped the turn")
	}
	if got := m.composer().String(); got != "/m" {
		t.Fatalf("composer = %q, want the draft untouched", got)
	}
}

func TestTheStopGestureIsSharedBetweenEscAndCtrlC(t *testing.T) {
	// What is counted is the reader stopping twice, not one key twice: a reader
	// who reaches for one and then the other is not making two mistakes.
	m := running(t, Config{Styles: testStyles(t)})

	updated, _ := m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	updated, _ = m.Update(press('c', tea.ModCtrl))
	m = updated.(Model)
	if m.cancel != nil {
		t.Fatal("esc then ctrl+c left the turn running")
	}
}

// TestTheQuestionOutranksTheStopGesture pins which of the two meanings Esc has
// when a question is on screen: the box owns the keys the turn is blocked on, so
// one press answers "no" and the turn keeps going. Whoever wants it to stop
// presses twice more, and the hint that advertises that is out of the way while
// the box is up.
func TestTheQuestionOutranksTheStopGesture(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t)})

	// The arm is already set when the question arrives: that is the sequence
	// where a stale one would hurt most, because the Esc that answers the box
	// would otherwise also be the second half of the stop.
	updated, _ := m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	if !m.interruptArmed {
		t.Fatal("the first press did not arm the stop")
	}

	question := make(chan AskAnswer, 1)
	updated, _ = m.Update(AskMsg{Request: AskRequest{Subject: "write?", Tool: "write"}, Reply: question})
	m = updated.(Model)
	if got := m.placeholder(); got != "" {
		t.Fatalf("placeholder under the question = %q, want none", got)
	}

	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	if m.approval != nil {
		t.Fatal("Esc left the question on screen")
	}
	select {
	case got := <-question:
		if got.Allowed {
			t.Fatalf("Esc approved the call: %+v", got)
		}
	default:
		t.Fatal("Esc did not answer the question")
	}
	if m.cancel == nil || !m.busy {
		t.Fatal("answering the question stopped the turn")
	}
	if m.interruptArmed {
		t.Fatal("the key the question took was also counted as the first stop")
	}
	if got := m.placeholder(); got != "esc twice to interrupt" {
		t.Fatalf("placeholder = %q, want the unarmed hint", got)
	}
}

func TestAFinishedTurnLeavesTheStopGestureDisarmed(t *testing.T) {
	// A turn that ends by itself must not leave the second press of a gesture
	// that started against it aimed at the next one.
	m := running(t, Config{Styles: testStyles(t)})
	updated, _ := m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)

	updated, _ = m.Update(TurnMsg{View: ViewMain})
	m = updated.(Model)
	if m.busy || m.interruptArmed {
		t.Fatalf("busy=%v armed=%v after the turn ended by itself", m.busy, m.interruptArmed)
	}
}

func TestInterruptGivesBackEverythingTheTurnHeld(t *testing.T) {
	// The keymap normally answers a modal first, so this state is not reachable
	// from the keyboard. It is reachable from every other caller of interrupt,
	// and a question left up would answer into a channel nobody reads while
	// eating the keys that follow.
	question := make(chan AskAnswer, 1)
	menu := make(chan PickAnswer, 1)
	m := sized(t, New(Config{Styles: testStyles(t)}))
	m.busy = true
	m.approval = &AskMsg{Request: AskRequest{Subject: "write?", Tool: "write"}, Reply: question}

	if closed := m.interrupt(); !closed {
		t.Fatal("interrupt did not report the box it closed")
	}
	if m.approval != nil {
		t.Fatal("the question outlived the turn")
	}
	select {
	case got := <-question:
		if got.Allowed {
			t.Fatalf("the abandoned question recorded a yes: %+v", got)
		}
	default:
		t.Fatal("the abandoned question left its caller blocked")
	}

	m.busy = true
	m.chooser = &PickMsg{Request: PickRequest{Title: "model"}, Reply: menu}
	m.interrupt()
	if m.chooser != nil {
		t.Fatal("the menu outlived the turn")
	}
	select {
	case answer := <-menu:
		if answer.Taken {
			t.Fatal("the abandoned menu answered with a row")
		}
	default:
		t.Fatal("the abandoned menu left its caller blocked")
	}
}

func TestInterruptPutsQueuedLinesBackInTheComposer(t *testing.T) {
	restored := []string{"second thought", "and another"}
	m := running(t, Config{
		Styles: testStyles(t),
		Interrupt: func() []string {
			lines := restored
			restored = nil
			return lines
		},
	})
	updated, _ := m.Update(typeText("half a draft"))
	m = updated.(Model)

	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)

	// The queued lines come first and the draft last: the draft is the newest
	// thing the reader said.
	if got, want := m.composer().String(), "second thought and another half a draft"; got != want {
		t.Fatalf("composer = %q, want %q", got, want)
	}
}

func TestModelScrollingClampsAndFollowsTheBottom(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))
	for i := 0; i < 200; i++ {
		m.views[ViewMain].transcript.Append(Block{Role: RoleNotice, Text: "line"})
	}

	updated, _ := m.Update(press(tea.KeyPgUp, 0))
	m = updated.(Model)
	if m.views[ViewMain].offset == 0 {
		t.Fatal("page up did not scroll")
	}

	updated, _ = m.Update(press(tea.KeyPgUp, tea.ModCtrl))
	m = updated.(Model)
	if m.views[ViewMain].offset == 0 {
		t.Fatal("page up with a modifier did not scroll either; the key map lost a case")
	}

	updated, _ = m.Update(press(tea.KeyEnd, tea.ModCtrl))
	m = updated.(Model)
	if m.views[ViewMain].offset != 0 {
		t.Fatalf("ctrl+end left the offset at %d, want the bottom", m.views[ViewMain].offset)
	}

	updated, _ = m.Update(press(tea.KeyHome, tea.ModCtrl))
	m = updated.(Model)
	if m.views[ViewMain].offset == 0 {
		t.Fatal("ctrl+home did not jump to the top")
	}
	want := m.maxOffset()
	if m.views[ViewMain].offset != want {
		t.Fatalf("offset = %d, want the maximum %d", m.views[ViewMain].offset, want)
	}

	lines := m.bodyLines()
	if len(lines) != m.bodyHeight() {
		t.Fatalf("body has %d lines, want %d", len(lines), m.bodyHeight())
	}
}

func TestModelKeepsEachViewSeparate(t *testing.T) {
	// The host owns which conversation the prompt is in; the model only keeps a
	// buffer per answer.
	target := ViewSide
	toggle := func() (int, string) { return target, "── side ──" }
	m := sized(t, New(Config{Styles: testStyles(t), Toggle: toggle}))

	updated, _ := m.Update(typeText("main draft"))
	m = updated.(Model)
	updated, _ = m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if m.active != ViewSide {
		t.Fatal("ctrl+o did not follow the host to the side view")
	}
	if !m.composer().Empty() {
		t.Fatal("the side view inherited the main draft")
	}

	updated, _ = m.Update(typeText("side draft"))
	m = updated.(Model)
	target = ViewMain
	updated, _ = m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if m.composer().String() != "main draft" {
		t.Fatalf("main draft = %q", m.composer().String())
	}
	m.active = ViewSide
	if m.composer().String() != "side draft" {
		t.Fatalf("side draft = %q", m.composer().String())
	}
}

func TestModelDoesNotSwitchViewsWithoutAHost(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if m.active != ViewMain {
		t.Fatal("the view switched with no host to switch it")
	}
}

func TestModelRefusesToSwitchDuringATurn(t *testing.T) {
	release := make(chan struct{})
	runner := func(ctx context.Context, line string, sink Sink) error {
		<-release
		return nil
	}
	switched := false
	toggle := func() (int, string) {
		switched = true
		return ViewSide, ""
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner, Toggle: toggle}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	updated, _ = m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if switched {
		t.Fatal("the view switched while a turn was running")
	}
	close(release)
	next(t, m)
}

func TestModelNotesOutputFromTheHost(t *testing.T) {
	runner := func(ctx context.Context, line string, sink Sink) error {
		sink.Note("model: mock/one")
		sink.Note("compacted 100 tokens into 20")
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("/model one"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	// The chain may also deliver a repaint request between the two notes, so
	// the loop reads until the transcript says both arrived.
	for i := 0; i < 8 && len(m.views[ViewMain].transcript.Blocks()) < 3; i++ {
		updated, _ = m.Update(next(t, m))
		m = updated.(Model)
	}
	blocks := m.views[ViewMain].transcript.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want the prompt and two notes: %+v", len(blocks), blocks)
	}
	if blocks[1].Role != RoleNotice || blocks[1].Text != "model: mock/one" {
		t.Fatalf("first note = %+v", blocks[1])
	}
	if blocks[2].Text != "compacted 100 tokens into 20" {
		t.Fatalf("second note = %+v", blocks[2])
	}
}

func TestModelReplaysAnAnswerAsOneBlock(t *testing.T) {
	// A resumed conversation arrives as answers, not as notes: the bar has to
	// run down the whole block, and only the transcript can draw that once it
	// has the text as a block instead of as one line at a time.
	runner := func(ctx context.Context, line string, sink Sink) error {
		sink.Answer("first line of the answer\nsecond line of it")
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("/resume 01a17840"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	for i := 0; i < 8 && len(m.views[ViewMain].transcript.Blocks()) < 2; i++ {
		updated, _ = m.Update(next(t, m))
		m = updated.(Model)
	}
	blocks := m.views[ViewMain].transcript.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want the prompt and the answer: %+v", len(blocks), blocks)
	}
	if blocks[1].Role != RoleAnswer {
		t.Fatalf("the replayed block has role %v, want an answer", blocks[1].Role)
	}
	if !strings.Contains(blocks[1].Text, "second line of it") {
		t.Fatalf("the replayed block lost the rest of the answer: %q", blocks[1].Text)
	}
	// The conversation's rows only: the composer's box draws the same character
	// down its left edge, and a bar count over the whole frame would be
	// counting the frame.
	body := strings.Split(plain(m.View().Content), "\n")[:m.bodyHeight()]
	if bars := strings.Count(strings.Join(body, "\n"), AnswerBar); bars != 2 {
		t.Fatalf("the transcript carries %d bars, want one per answer line: %q", bars, body)
	}
}

func TestModelAsksForOneFullRepaintPerBurstOfNotes(t *testing.T) {
	// The incremental painter moves lines it believes moved; a burst of notices
	// is where that goes wrong. One repaint request per burst is the price of
	// trusting the screen at the end of it, and it has to travel through the
	// same reader as everything else: a second read would be a second reader.
	runner := func(ctx context.Context, line string, sink Sink) error {
		sink.Note("first")
		sink.Note("second")
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("/help"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	repaints, notes := 0, 0
	for i := 0; i < 10 && repaints == 0; i++ {
		msg := next(t, m)
		if _, ok := msg.(NoteMsg); ok {
			notes++
		}
		updated, cmd := m.Update(msg)
		m = updated.(Model)
		if _, ok := msg.(clearRequest); !ok {
			continue
		}
		// The request is half of it: the command it hands back is what asks
		// the renderer to paint the whole screen again.
		repaints++
		if cmd == nil {
			t.Fatal("the repaint request produced no command")
		}
		if sent := cmd(); sent != tea.ClearScreen() {
			t.Fatalf("the repaint command returned %T, want a clear", sent)
		}
		updated, _ = m.Update(cmd())
		m = updated.(Model)
	}
	if notes != 2 {
		t.Fatalf("delivered %d notes, want 2", notes)
	}
	if repaints != 1 {
		t.Fatalf("got %d repaint requests for one burst, want 1", repaints)
	}
	if !m.host.armed {
		t.Fatal("the reader stopped: nothing is waiting on the channel")
	}
}

func TestModelAsksForAFullRepaintWhenTheModalCloses(t *testing.T) {
	// Answering a question removes a box that covered part of the transcript.
	// The rows underneath are not the rows that were drawn there — the box had
	// its own — so the incremental painter needs to be told to repaint.
	runner := func(ctx context.Context, line string, sink Sink) error {
		sink.Ask(AskRequest{Subject: "Ran  bash  ls", Tool: "bash"})
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if m.approval == nil {
		t.Fatal("no modal after the gate asked")
	}

	updated, _ = m.Update(typeText("y"))
	m = updated.(Model)
	if !m.host.queued {
		t.Fatal("answering the question did not queue a repaint")
	}
	if m.approval != nil {
		t.Fatal("the modal stayed up after answering")
	}
}

func TestCtrlCStepsOutOfTheSideConversation(t *testing.T) {
	// One level in, one press out. Before this the key armed the exit in both
	// views, so the second press ended the session and took the main
	// conversation — the one the reader was planning to come back to — with it.
	active := ViewSide
	back := func() (int, string, bool) {
		if active != ViewSide {
			return ViewMain, "", false
		}
		active = ViewMain
		return ViewMain, "── main ── the side conversation is kept", true
	}
	m := sized(t, New(Config{Styles: testStyles(t), Back: back}))
	m.active = ViewSide

	updated, _ := m.Update(press('c', tea.ModCtrl))
	m = updated.(Model)
	if m.active != ViewMain {
		t.Fatal("ctrl+c did not step out of the side conversation")
	}
	if m.quit || m.exitArmed {
		t.Fatalf("stepping back quit=%v armed=%v, want neither", m.quit, m.exitArmed)
	}
	blocks := m.views[ViewMain].transcript.Blocks()
	if len(blocks) != 1 || blocks[0].Role != RoleNotice || !strings.Contains(blocks[0].Text, "kept") {
		t.Fatalf("the main view was not told where the prompt landed: %+v", blocks)
	}

	// The conversation underneath keeps its own gesture: two presses, and the
	// first one only arms.
	updated, _ = m.Update(press('c', tea.ModCtrl))
	m = updated.(Model)
	if m.quit || !m.exitArmed {
		t.Fatalf("the first press in main quit=%v armed=%v, want it armed", m.quit, m.exitArmed)
	}
	updated, cmd := m.Update(press('c', tea.ModCtrl))
	if cmd == nil || !updated.(Model).quit {
		t.Fatal("the second press in main did not quit")
	}
}

func TestCtrlCOnlyInterruptsWhileATurnRuns(t *testing.T) {
	release := make(chan struct{})
	stepped := false
	runner := func(ctx context.Context, line string, sink Sink) error {
		<-release
		return nil
	}
	back := func() (int, string, bool) {
		stepped = true
		return ViewMain, "", true
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner, Back: back}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	updated, _ = m.Update(press('c', tea.ModCtrl))
	m = updated.(Model)
	if stepped {
		t.Fatal("ctrl+c stepped out of a view instead of stopping the turn")
	}
	if !m.busy {
		t.Fatal("the turn ended; the key was supposed to cancel it")
	}
	close(release)
	next(t, m)
}

func TestModelQuitsOnTheSecondCtrlC(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))

	// An empty prompt costs two presses as well: the key that stops a turn and
	// the key that ends a session should not be one press apart.
	updated, cmd := m.Update(press('c', tea.ModCtrl))
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("ctrl+c quit an idle session on the first press")
	}
	if !m.exitArmed {
		t.Fatal("the first press did not arm the exit")
	}
	if got, want := m.placeholder(), "ctrl+c again to exit"; got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}

	// Anything else puts the reader back to work.
	updated, _ = m.Update(typeText("x"))
	m = updated.(Model)
	if m.exitArmed {
		t.Fatal("a keypress left the exit armed")
	}
	updated, cmd = m.Update(press('c', tea.ModCtrl))
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("ctrl+c with a draft quit instead of clearing it")
	}
	if !m.exitArmed {
		t.Fatal("ctrl+c with a draft did not arm the exit")
	}
	if m.composer().String() != "" {
		t.Fatalf("ctrl+c left %q behind", m.composer().String())
	}

	updated, cmd = m.Update(press('c', tea.ModCtrl))
	if cmd == nil || !updated.(Model).quit {
		t.Fatal("the second ctrl+c did not quit")
	}
}

func TestModelQuitsImmediatelyOnCtrlD(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, cmd := m.Update(press('d', tea.ModCtrl))
	if cmd == nil || !updated.(Model).quit {
		t.Fatal("ctrl+d on an empty prompt did not quit")
	}
}

// home is the host side of the welcome page: one page for every view, which is
// what a session with a side conversation has.
func home(info *Welcome) func(int) *Welcome {
	return func(int) *Welcome { return info }
}

func TestWelcomeFillsTheScreenUntilSomethingIsSaid(t *testing.T) {
	info := &Welcome{Version: "9.9.9", Dir: "~/proj/atlas", Model: "fake/one", Note: "think high · fs+bash"}
	m := sized(t, New(Config{Styles: testStyles(t), Welcome: home(info)}))
	frame := m.View().Content
	for _, want := range []string{"9.9.9", "~/proj/atlas", "fake/one", "fs+bash", "Ctrl+C twice exits"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the home screen does not mention %q:\n%s", want, frame)
		}
	}
	if lines := strings.Count(frame, "\n") + 1; lines != m.height {
		t.Fatalf("frame has %d lines, want %d", lines, m.height)
	}

	// The page is a placeholder for the first line of a conversation, so the
	// first line is what ends it.
	updated, _ := m.Update(NoteMsg{Text: "new session: #1"})
	m = updated.(Model)
	if strings.Contains(m.View().Content, "9.9.9") {
		t.Fatal("the home screen outlived the first line")
	}
}

// TestTheHomePageDrawsTheWordmarkInBlocks covers the one glyph on the page that
// is not text. The mark is drawn as blocks so the letters read as shapes — the
// figlet face it replaces left only strokes, and on a screen with nothing else
// to look at a reader had to guess which stroke belonged to which letter — and
// the rows are what pins it down: every row carries the same number of blocks
// and starts at the same column, which is what keeps the letters in line even
// where the terminal disagrees about how wide a block is.
func TestTheHomePageDrawsTheWordmarkInBlocks(t *testing.T) {
	info := &Welcome{Version: "9.9.9", Dir: "~/proj/atlas", Model: "fake/one"}
	m := sized(t, New(Config{Styles: testStyles(t), Welcome: home(info)}))
	frame := plain(m.View().Content)

	mark := make([]string, 0, len(logo))
	for _, row := range strings.Split(frame, "\n") {
		if strings.Contains(row, "█") {
			mark = append(mark, strings.TrimRight(row, " "))
		}
	}
	if len(mark) != len(logo) {
		t.Fatalf("the page drew %d rows of blocks, want %d:\n%s", len(mark), len(logo), frame)
	}
	lead := func(row string) int {
		return lipgloss.Width(row) - lipgloss.Width(strings.TrimLeft(row, " "))
	}
	indent := lead(mark[0])
	for i, row := range mark {
		if got, want := strings.TrimLeft(row, " "), strings.TrimRight(logo[i], " "); got != want {
			t.Fatalf("row %d of the wordmark is %q, want %q:\n%s", i, got, want, frame)
		}
		if at := lead(row); at != indent {
			t.Fatalf("row %d starts at column %d, want %d:\n%s", i, at, indent, frame)
		}
	}

	// Two cells of margin on either side is what "the mark fits" means here: a
	// mark against the edge reads as cropped, so a screen one cell short of that
	// gets the version line instead.
	narrow := New(Config{Styles: testStyles(t), Welcome: home(info)})
	updated, _ := narrow.Update(tea.WindowSizeMsg{Width: wordmarkWidth() + 3, Height: 20})
	narrow = updated.(Model)
	if strings.Contains(narrow.View().Content, "█") {
		t.Fatalf("the wordmark did not give way on a %d-column screen:\n%s", wordmarkWidth()+3, plain(narrow.View().Content))
	}
	if !strings.Contains(narrow.View().Content, "9.9.9") {
		t.Fatal("the narrow page lost the version as well as the wordmark")
	}
}

// TestTheHomePageCanSayWhatJustHappened covers the receipt for a command that
// leaves the session empty — /archive and /delete have nowhere to put a line,
// because the transcript is the thing the page replaces. The line goes on the
// page, beside the facts, and it is what tells the reader the archive happened
// rather than a transcript block that would push the page away.
func TestTheHomePageCanSayWhatJustHappened(t *testing.T) {
	info := &Welcome{Version: "9.9.9", Dir: "~/proj/atlas", Model: "fake/one", Note: "think high"}
	runner := func(context.Context, string, Sink) error { return nil }
	m := sized(t, New(Config{Styles: testStyles(t), Welcome: home(info), Run: runner}))

	updated, _ := m.Update(FlashMsg{Text: "archived: session 01a17b1f · /recover opens it again"})
	m = updated.(Model)
	frame := m.View().Content
	if !strings.Contains(frame, "archived: session 01a17b1f") {
		t.Fatalf("the page says nothing about the archive:\n%s", frame)
	}
	// The page's own facts stay: the flash is added to them rather than swapped
	// in for them.
	for _, want := range []string{"9.9.9", "fake/one", "think high", "Ctrl+C twice exits"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the flash cost the page %q:\n%s", want, frame)
		}
	}

	// Saying something spends it: by then the last event is the turn that is
	// running, and the page is not on screen to say it.
	m.composer().Insert("hi")
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	if m.flash != "" {
		t.Fatalf("the submitted line left %q on the page", m.flash)
	}
}

// TestTheFlashStaysOutOfTheSideConversation: the line reports what happened to
// the session, and the side view is a page of its own. Repeating it there would
// describe an event the reader did not just cause in the conversation on screen.
func TestTheFlashStaysOutOfTheSideConversation(t *testing.T) {
	info := &Welcome{Version: "9.9.9", Dir: "~/proj/atlas", Model: "fake/one"}
	sidePage := &Welcome{Title: "side conversation · 3 messages inherited", Model: "fake/one"}
	pages := func(view int) *Welcome {
		if view == ViewSide {
			return sidePage
		}
		return info
	}
	toggle := func() (int, string) { return ViewSide, "" }
	m := sized(t, New(Config{Styles: testStyles(t), Welcome: pages, Toggle: toggle}))

	updated, _ := m.Update(FlashMsg{Text: "archived: session 01a17b1f · /recover opens it again"})
	m = updated.(Model)
	updated, _ = m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if m.active != ViewSide {
		t.Fatal("ctrl+o did not open the side view")
	}
	frame := m.View().Content
	if !strings.Contains(frame, "3 messages inherited") {
		t.Fatalf("the side view lost its own page:\n%s", frame)
	}
	if strings.Contains(frame, "archived: session") {
		t.Fatalf("the side page repeated the session's last event:\n%s", frame)
	}
}

func TestWelcomeShrinksToTheVersionWhenTheScreenIsShort(t *testing.T) {
	info := &Welcome{Version: "9.9.9", Dir: "~/proj/atlas", Model: "fake/one"}
	m := New(Config{Styles: testStyles(t), Welcome: home(info)})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 6})
	m = updated.(Model)
	frame := m.View().Content
	if !strings.Contains(frame, "9.9.9") {
		t.Fatalf("the cramped home screen lost the version:\n%s", frame)
	}
	if strings.Contains(frame, "fake/one") {
		t.Fatalf("the cramped home screen kept the model line:\n%s", frame)
	}
	if lines := strings.Count(frame, "\n") + 1; lines != 6 {
		t.Fatalf("frame has %d lines, want 6", lines)
	}
}

func TestTheSideConversationGetsItsOwnHomePage(t *testing.T) {
	// A side conversation opens mid-session, with history already behind it, so
	// its page cannot be the one the session started with: the wordmark and the
	// version say nothing about a conversation that was just carved out of
	// another. The page is asked for per view, because the count it reports is
	// only known once the host has opened it.
	mainPage := &Welcome{Version: "9.9.9", Dir: "~/proj/atlas", Model: "fake/one"}
	sidePage := &Welcome{Title: "side conversation · 3 messages inherited", Model: "fake/one"}
	pages := func(view int) *Welcome {
		if view == ViewSide {
			return sidePage
		}
		return mainPage
	}
	target := ViewSide
	toggle := func() (int, string) { return target, "" }
	m := sized(t, New(Config{Styles: testStyles(t), Welcome: pages, Toggle: toggle}))

	if !strings.Contains(m.View().Content, "9.9.9") {
		t.Fatal("the main view lost its home page")
	}
	// From here the session has a line of its own, which is what the page was
	// standing in for.
	m.views[ViewMain].transcript.Append(Block{Role: RoleUser, Text: "what is in this directory?"})
	updated, _ := m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if m.active != ViewSide {
		t.Fatal("ctrl+o did not open the side view")
	}

	frame := m.View().Content
	if !strings.Contains(frame, "3 messages inherited") {
		t.Fatalf("the side view has no page of its own:\n%s", frame)
	}
	if strings.Contains(frame, "9.9.9") {
		t.Fatalf("the side view repeated the session's version line:\n%s", frame)
	}
	if !strings.Contains(frame, "Ctrl+C or Ctrl+O goes back") {
		t.Fatalf("the side page does not say how to leave it:\n%s", frame)
	}

	// The page is a placeholder for the first line of that conversation, and the
	// side one starts empty — which is the only case where it shows at all.
	m.views[ViewSide].transcript.Append(Block{Role: RoleNotice, Text: "── side ──"})
	if strings.Contains(m.View().Content, "3 messages inherited") {
		t.Fatal("the side page outlived the first line")
	}

	// Coming back, the main conversation is where it was left: its page is gone
	// as well, since it has something to say.
	target = ViewMain
	updated, _ = m.Update(press('o', tea.ModCtrl))
	m = updated.(Model)
	if strings.Contains(m.View().Content, "9.9.9") {
		t.Fatal("the main page came back over a transcript that is not empty")
	}
}

// commands is the palette a host would offer: two names, one of them with an
// argument.
func palette(line string) []Suggestion {
	field, _, _ := strings.Cut(line, " ")
	if !strings.HasPrefix(field, "/") {
		return nil
	}
	out := make([]Suggestion, 0, 2)
	for _, pick := range []Suggestion{
		{Name: "/model", Usage: "<name>[:level]", About: "switch model, or pick one from the list"},
		{Name: "/new", About: "start a new session in this directory"},
	} {
		if strings.HasPrefix(pick.Name, field) {
			out = append(out, pick)
		}
	}
	return out
}

func TestPalettePutsTheDescriptionBesideTheName(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette}))
	updated, _ := m.Update(typeText("/"))
	m = updated.(Model)

	lines := m.suggestionLines()
	if len(lines) != 2 {
		t.Fatalf("palette has %d rows, want 2: %v", len(lines), lines)
	}
	for _, want := range []string{"/model", "<name>[:level]", "switch model, or pick one from the list"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("row 0 = %q, missing %q", lines[0], want)
		}
	}
	if !strings.Contains(lines[0], "▸ ") {
		t.Fatalf("the picked row is not marked: %q", lines[0])
	}
	if strings.Contains(lines[1], "▸ ") {
		t.Fatalf("two rows are marked: %q", lines[1])
	}
	if strings.Index(lines[0], "switch model") < strings.Index(lines[0], "/model") {
		t.Fatalf("the description is not in the second column: %q", lines[0])
	}
	// The palette costs rows, not an overflow: the frame is still the screen.
	if frame := m.View().Content; strings.Count(frame, "\n")+1 != m.height {
		t.Fatalf("frame has %d lines, want %d", strings.Count(frame, "\n")+1, m.height)
	}
}

func TestPaletteTakesTheArrowsAndTabFillsTheName(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette}))
	updated, _ := m.Update(typeText("/"))
	m = updated.(Model)

	updated, _ = m.Update(press(tea.KeyDown, 0))
	m = updated.(Model)
	if m.picked != 1 {
		t.Fatalf("down left the highlight on %d", m.picked)
	}
	updated, _ = m.Update(press(tea.KeyTab, 0))
	m = updated.(Model)
	if got, want := m.composer().String(), "/new"; got != want {
		t.Fatalf("tab filled %q, want %q", got, want)
	}
}

func TestEnterCompletesAnUnfinishedCommand(t *testing.T) {
	lines := make(chan string, 1)
	runner := func(ctx context.Context, line string, sink Sink) error {
		lines <- line
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette, Run: runner}))
	updated, _ := m.Update(typeText("/mo"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))

	select {
	case got := <-lines:
		if got != "/model" {
			t.Fatalf("submitted %q, want %q", got, "/model")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never started")
	}
}

// TestEnterKeepsTheArgumentItWasGiven: 0.4.1 compared only the first field, so
// "/rename alpha" was rewritten to "/rename " on the way in and the command ran
// without its argument.
func TestEnterKeepsTheArgumentItWasGiven(t *testing.T) {
	lines := make(chan string, 2)
	runner := func(ctx context.Context, line string, sink Sink) error {
		lines <- line
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette, Run: runner}))
	updated, _ := m.Update(typeText("/model deep"))
	m = updated.(Model)

	// Tab only fills, and there is nothing to fill: the line is not a prefix of
	// any command.
	updated, _ = m.Update(press(tea.KeyTab, 0))
	m = updated.(Model)
	if got, want := m.composer().String(), "/model deep"; got != want {
		t.Fatalf("Tab rewrote the line to %q, want %q", got, want)
	}

	updated, _ = m.Update(press(tea.KeyEnter, 0))
	select {
	case got := <-lines:
		if got != "/model deep" {
			t.Fatalf("submitted %q, want the argument kept", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never started")
	}
}

func TestPaletteDropsTheDescriptionWhenThereIsNoRoom(t *testing.T) {
	m := New(Config{Styles: testStyles(t), Suggest: palette})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 26, Height: 10})
	m = updated.(Model)
	updated, _ = m.Update(typeText("/"))
	m = updated.(Model)

	lines := m.suggestionLines()
	if len(lines) != 2 {
		t.Fatalf("palette has %d rows, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "/model") {
		t.Fatalf("the name went missing: %q", lines[0])
	}
	if strings.Contains(lines[0], "switch model") {
		t.Fatalf("a fragment of the description survived: %q", lines[0])
	}
}

func TestPaletteStaysOutOfAnOrdinaryPrompt(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette}))
	updated, _ := m.Update(typeText("hello"))
	m = updated.(Model)
	if lines := m.suggestionLines(); len(lines) != 0 {
		t.Fatalf("a prompt grew a palette: %v", lines)
	}
}

func TestPaletteYieldsToTheApprovalModal(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette}))
	updated, _ := m.Update(typeText("/"))
	m = updated.(Model)
	updated, _ = m.Update(AskMsg{Request: AskRequest{Subject: "bash  rm -rf build", Tool: "bash"}, Reply: make(chan AskAnswer, 1)})
	m = updated.(Model)
	if lines := m.suggestionLines(); len(lines) != 0 {
		t.Fatalf("the palette stayed up under the modal: %v", lines)
	}
}

// pickMenu builds one list. The labels repeat the values because a test only
// has to tell the rows apart.
func pickMenu(title, current string, values ...string) PickMsg {
	items := make([]Choice, len(values))
	for i, value := range values {
		items[i] = Choice{Value: value, Label: value, Note: "note for " + value}
	}
	return PickMsg{
		Request: PickRequest{Title: title, Items: items, Current: current},
		Reply:   make(chan PickAnswer, 1),
	}
}

func TestPickDrawsTheRowsAndTakesTheHighlightedOne(t *testing.T) {
	msg := pickMenu("model", "", "alpha", "beta")
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(msg)
	m = updated.(Model)

	frame := plain(m.View().Content)
	for _, want := range []string{"model", "▸ alpha", "beta", "note for beta", "↑↓ choose"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the menu is missing %q:\n%s", want, frame)
		}
	}
	// The box replaces rows rather than adding them: the frame is still exactly
	// one screen.
	if lines := strings.Count(m.View().Content, "\n") + 1; lines != m.height {
		t.Fatalf("frame has %d lines, want %d", lines, m.height)
	}
	// The composer takes no keys while the box is up, so it stops advertising
	// one: "esc twice to interrupt" beside a footer that says "Esc leaves"
	// would read as two different meanings for the same key.
	if hint := m.placeholder(); hint != "" {
		t.Fatalf("placeholder while the list is up = %q, want none", hint)
	}

	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	select {
	case answer := <-msg.Reply:
		if !answer.Taken || answer.Value != "alpha" {
			t.Fatalf("answer = %+v, want alpha", answer)
		}
	default:
		t.Fatal("Enter closed the list without answering")
	}
	if m.chooser != nil {
		t.Fatal("the list is still up after it was answered")
	}
}

func TestPickOpensOnTheRowInEffect(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(pickMenu("model", "beta", "alpha", "beta", "gamma"))
	m = updated.(Model)

	if m.chosen != 1 {
		t.Fatalf("the list opened on %d, want the row in effect", m.chosen)
	}
	frame := plain(m.View().Content)
	if !strings.Contains(frame, "▸ beta") {
		t.Fatalf("the row in effect is not the marked one:\n%s", frame)
	}
}

func TestPickArrowsWrapAndTheWindowFollows(t *testing.T) {
	values := make([]string, 20)
	for i := range values {
		values[i] = fmt.Sprintf("m%02d", i)
	}
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(pickMenu("resume", "", values...))
	m = updated.(Model)

	// Up from the first row is the last one: the menu wraps the way the command
	// palette does.
	updated, _ = m.Update(press(tea.KeyUp, 0))
	m = updated.(Model)
	if m.chosen != 19 {
		t.Fatalf("up from the top landed on %d, want 19", m.chosen)
	}
	if frame := plain(m.View().Content); !strings.Contains(frame, "11–20 of 20") {
		t.Fatalf("the title does not say where the window is:\n%s", frame)
	}

	updated, _ = m.Update(press(tea.KeyDown, 0))
	m = updated.(Model)
	if m.chosen != 0 {
		t.Fatalf("down from the bottom landed on %d, want 0", m.chosen)
	}

	// Eleven rows further down: the highlight stays on screen and the rows it
	// left are gone.
	for i := 0; i < 11; i++ {
		updated, _ = m.Update(press(tea.KeyDown, 0))
		m = updated.(Model)
	}
	if m.chosen != 11 {
		t.Fatalf("eleven rows down landed on %d", m.chosen)
	}
	frame := plain(m.View().Content)
	if !strings.Contains(frame, "▸ m11") {
		t.Fatalf("the highlight scrolled off the window:\n%s", frame)
	}
	if strings.Contains(frame, "m01") {
		t.Fatalf("a row outside the window is still drawn:\n%s", frame)
	}

	updated, _ = m.Update(press(tea.KeyEnd, 0))
	m = updated.(Model)
	if m.chosen != 19 {
		t.Fatalf("end landed on %d", m.chosen)
	}
	updated, _ = m.Update(press(tea.KeyHome, 0))
	m = updated.(Model)
	if m.chosen != 0 {
		t.Fatalf("home landed on %d", m.chosen)
	}
}

// pickAxisMenu builds a list whose rows carry a horizontal axis, the shape the
// model menu has: one row per model, each with its own positions.
func pickAxisMenu(axis, title, current string, rows []Choice) PickMsg {
	return PickMsg{
		Request: PickRequest{Title: title, Items: rows, Current: current, Axis: axis},
		Reply:   make(chan PickAnswer, 1),
	}
}

func TestPickTurnsTheAxisAndStopsAtTheEnds(t *testing.T) {
	msg := pickAxisMenu("thinking", "model", "", []Choice{
		{Value: "alpha", Label: "alpha", Steps: []string{"off", "low", "high", "max"}, Step: 1},
	})
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(msg)
	m = updated.(Model)

	frame := plain(m.View().Content)
	if !strings.Contains(frame, "‹ low ›") {
		t.Fatalf("the axis does not open where the row says it is:\n%s", frame)
	}
	// The footer is the only place that says which key turns the knob, and it
	// only says so when the host asked for an axis.
	if !strings.Contains(frame, "←→ thinking") {
		t.Fatalf("the footer does not advertise the axis:\n%s", frame)
	}

	updated, _ = m.Update(press(tea.KeyRight, 0))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyRight, 0))
	m = updated.(Model)
	if frame := plain(m.View().Content); !strings.Contains(frame, "‹ max ›") {
		t.Fatalf("two rights did not reach max:\n%s", frame)
	}

	// Unlike the rows, the axis does not wrap: it is a scale, so one press past
	// "max" stays on max rather than falling back to "off".
	updated, _ = m.Update(press(tea.KeyRight, 0))
	m = updated.(Model)
	if frame := plain(m.View().Content); !strings.Contains(frame, "‹ max ›") {
		t.Fatalf("right past the end did not stop:\n%s", frame)
	}
	for i := 0; i < 6; i++ {
		updated, _ = m.Update(press(tea.KeyLeft, 0))
		m = updated.(Model)
	}
	if frame := plain(m.View().Content); !strings.Contains(frame, "‹ off ›") {
		t.Fatalf("left past the start did not stop:\n%s", frame)
	}

	// The box keeps its size while the axis turns, so the answer is still the
	// one row, on the position the presses left it.
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	select {
	case answer := <-msg.Reply:
		if !answer.Taken || answer.Value != "alpha" || answer.Step != 0 {
			t.Fatalf("answer = %+v, want alpha on step 0", answer)
		}
	default:
		t.Fatal("Enter closed the list without answering")
	}
}

func TestPickTakesEachRowWithItsOwnStep(t *testing.T) {
	// Two rows with different gears: the axis belongs to the row, so the press
	// that runs one row to its end must not touch the other.
	msg := pickAxisMenu("thinking", "model", "", []Choice{
		{Value: "alpha", Label: "alpha", Steps: []string{"off", "low", "high", "max"}, Step: 0},
		{Value: "beta", Label: "beta", Steps: []string{"off", "high"}, Step: 1},
	})
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(msg)
	m = updated.(Model)

	updated, _ = m.Update(press(tea.KeyRight, 0))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyDown, 0))
	m = updated.(Model)
	if frame := plain(m.View().Content); !strings.Contains(frame, "▸ beta") {
		t.Fatalf("the highlight did not move to the second row:\n%s", frame)
	}
	// The second row's own position is drawn on it while the first keeps the
	// one it was left on, right-aligned in the same column.
	if frame := plain(m.View().Content); !strings.Contains(frame, "‹ high ›") {
		t.Fatalf("the second row opened on the wrong gear:\n%s", frame)
	}

	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	select {
	case answer := <-msg.Reply:
		if !answer.Taken || answer.Value != "beta" || answer.Step != 1 {
			t.Fatalf("answer = %+v, want beta on step 1", answer)
		}
	default:
		t.Fatal("Enter closed the list without answering")
	}
}

func TestPickIgnoresTheAxisKeysOnARowWithoutOne(t *testing.T) {
	// A list the host gave no axis for: the session menu. The keys are "up" and
	// "down" there, so ←/→ have nothing to move and are not advertised.
	msg := pickMenu("resume", "", "alpha", "beta")
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if frame := plain(m.View().Content); strings.Contains(frame, "←→") {
		t.Fatalf("the footer advertises an axis the list does not have:\n%s", frame)
	}
	updated, _ = m.Update(press(tea.KeyRight, 0))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyLeft, 0))
	m = updated.(Model)
	if m.chooser == nil || m.chosen != 0 {
		t.Fatalf("an axis key moved the list: chooser=%v chosen=%d", m.chooser != nil, m.chosen)
	}
	select {
	case answer := <-msg.Reply:
		t.Fatalf("an axis key answered the list: %+v", answer)
	default:
	}

	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	select {
	case answer := <-msg.Reply:
		if !answer.Taken || answer.Value != "alpha" || answer.Step != 0 {
			t.Fatalf("answer = %+v, want alpha on step 0", answer)
		}
	default:
		t.Fatal("Enter closed the list without answering")
	}
}

func TestPickEscapeLeavesTheListWithoutAnAnswer(t *testing.T) {
	msg := pickMenu("model", "", "alpha")
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(msg)
	m = updated.(Model)

	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	select {
	case answer := <-msg.Reply:
		if answer.Taken {
			t.Fatalf("Escape answered with %+v", answer)
		}
	default:
		t.Fatal("Escape left the caller blocked")
	}
	if m.chooser != nil {
		t.Fatal("the list survived Escape")
	}
}

func TestPickSwallowsKeysItDoesNotUse(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette}))
	updated, _ := m.Update(pickMenu("model", "", "alpha"))
	m = updated.(Model)
	updated, _ = m.Update(typeText("/"))
	m = updated.(Model)

	if got := m.composer().String(); got != "" {
		t.Fatalf("a key reached the composer behind the menu: %q", got)
	}
	if m.chooser == nil {
		t.Fatal("a key closed the list")
	}
	// The rest of the frame gives way: one modal, one answer.
	if lines := m.suggestionLines(); len(lines) != 0 {
		t.Fatalf("the palette drew under the menu: %v", lines)
	}
	if _, _, ok := m.caret(); ok {
		t.Fatal("the caret is showing behind the menu")
	}
}

func TestSecondPickIsRefusedWhileTheFirstIsUp(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))
	first, second := pickMenu("model", "", "alpha"), pickMenu("resume", "", "beta")
	updated, _ := m.Update(first)
	m = updated.(Model)
	updated, _ = m.Update(second)
	m = updated.(Model)

	select {
	case answer := <-second.Reply:
		if answer.Taken {
			t.Fatalf("the second list was answered: %+v", answer)
		}
	default:
		t.Fatal("the second caller is still blocked")
	}
	if items := m.chooser.Request.Items; len(items) != 1 || items[0].Value != "alpha" {
		t.Fatalf("the second list took the frame: %+v", items)
	}
}

func TestSinkPickBlocksUntilTheRowIsTaken(t *testing.T) {
	answers := make(chan PickAnswer, 1)
	runner := func(ctx context.Context, line string, sink Sink) error {
		answers <- sink.Pick(PickRequest{Title: "model", Items: []Choice{{Value: "a", Label: "alpha"}}})
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("/model"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	// The question travels the way every other one does: a message on the
	// channel the single reader owns.
	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if m.chooser == nil {
		t.Fatal("the command did not raise a list")
	}
	updated, _ = m.Update(press(tea.KeyEnter, 0))

	select {
	case answer := <-answers:
		if !answer.Taken || answer.Value != "a" {
			t.Fatalf("the sink answered %+v", answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command is still waiting for a row")
	}
}

func TestModelReportsARefusedTurn(t *testing.T) {
	runner := func(ctx context.Context, line string, sink Sink) error {
		return errors.New("the kernel is busy")
	}
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	updated, _ = m.Update(next(t, m))
	m = updated.(Model)

	blocks := m.views[ViewMain].transcript.Blocks()
	last := blocks[len(blocks)-1]
	if last.Role != RoleNotice || !strings.Contains(last.Text, "busy") {
		t.Fatalf("last block = %+v", last)
	}
}

func TestViewSetsTheTerminalModes(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Title: "atlas"}))
	m.View()
	view := m.View()
	if !view.AltScreen {
		t.Fatal("the interface does not use the alternate screen")
	}
	if view.WindowTitle != "atlas" {
		t.Fatalf("window title = %q", view.WindowTitle)
	}
	if view.Cursor == nil {
		t.Fatal("no caret in an idle composer")
	}
	if lines := strings.Count(view.Content, "\n") + 1; lines != m.height {
		t.Fatalf("frame has %d lines, want %d", lines, m.height)
	}
}

func TestModelSubmittingKeepsTheOneReader(t *testing.T) {
	// The reader belongs to the loop and is already armed when the key arrives.
	// A reader of the model's own would be the second one on the channel, and
	// two readers deliver messages in whatever order their goroutines wake: a
	// view switch overtaking the note that belongs to it, an answer split in
	// two.
	runner := func(ctx context.Context, line string, sink Sink) error { return nil }
	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	if !m.host.armed {
		t.Fatal("the idle model is not listening")
	}
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, cmd := m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	if !m.host.armed {
		t.Fatal("submitting disarmed the reader")
	}
	// Submitting does return one command now — the first beat of the busy clock
	// — so the check is not "no command at all" but "nothing else reads the
	// channel". A second reader would be armed by a second listen(), and the
	// armed flag is what says there is only one.
	if cmd == nil {
		t.Fatal("submitting did not start the busy clock")
	}
	if !m.busy {
		t.Fatal("submitting did not mark the turn as running")
	}
}

func TestModelKeepsOneListenerDuringATurn(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := func(ctx context.Context, line string, sink Sink) error {
		close(started)
		<-release
		return nil
	}

	m := sized(t, New(Config{Styles: testStyles(t), Run: runner}))
	updated, _ := m.Update(typeText("go"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	<-started
	if !m.host.armed {
		t.Fatal("no listener was armed for the turn")
	}

	// A key pressed during a turn must not arm a second reader: the first one is
	// still blocked on the channel, and a second one would survive the turn as a
	// goroutine nobody can reach.
	for _, key := range []tea.KeyPressMsg{typeText("x"), press(tea.KeyLeft, 0)} {
		updated, extra := m.Update(key)
		m = updated.(Model)
		if extra != nil {
			t.Fatal("a key press during a turn armed a second listener")
		}
		if !m.host.armed {
			t.Fatal("the event chain was disarmed by a key press")
		}
	}

	close(release)
	updated, _ = m.Update(next(t, m))
	m = updated.(Model)
	if !m.host.armed {
		t.Fatal("the listener was disarmed; notes between turns would be lost")
	}
}

// answered is a model with one exchange in its transcript, which is the screen
// the gap between the answer and the prompt is about: an empty transcript ends
// in blank rows anyway, so it could not tell a gap from padding.
func answered(t *testing.T) Model {
	t.Helper()
	return withLayout(t, func(*Layout) {}, testStyles(t), palette)
}

// The input area does not sit flush against the conversation. A blank row
// between them is what tells the reader that the last line of an answer is an
// answer and not the line being typed; without it a wrapped answer reads as if
// its final row were the prompt.
func TestTheInputAreaIsSeparatedFromTheConversation(t *testing.T) {
	m := answered(t)

	if m.cfg.Layout.ComposerGap < 1 {
		t.Fatal("the composer sits flush against the transcript")
	}
	if got, want := m.inputTop(), m.bodyHeight()+m.cfg.Layout.ComposerGap; got != want {
		t.Fatalf("input top = %d, want %d", got, want)
	}
	frame := strings.Split(plain(m.View().Content), "\n")
	if len(frame) != m.height {
		t.Fatalf("frame has %d rows, want %d", len(frame), m.height)
	}
	// The row above the gap carries the answer, so the blank row is between two
	// things and not just the top of the input area.
	if got := strings.TrimSpace(frame[m.bodyHeight()-1]); got == "" {
		t.Fatalf("row %d is blank, want the last row of the transcript", m.bodyHeight()-1)
	}
	for row := m.bodyHeight(); row < m.inputTop(); row++ {
		if got := strings.TrimSpace(frame[row]); got != "" {
			t.Fatalf("row %d = %q, want the blank gap", row, got)
		}
	}
	top := m.inputTop() + m.cfg.Layout.composerTop(m.width)
	if got := frame[m.inputTop()]; !strings.Contains(got, "╭") {
		t.Fatalf("row %d = %q, want the top of the input box under the gap", m.inputTop(), got)
	}
	if got := frame[top]; !strings.Contains(got, ">") {
		t.Fatalf("row %d = %q, want the prompt inside the box", top, got)
	}
	// The caret and the frame are built from the same helpers, so a gap in one
	// and not in the other would put the cursor on the wrong row.
	row, column, ok := m.caret()
	if !ok {
		t.Fatal("no caret on an idle screen")
	}
	if row != top {
		t.Fatalf("caret row = %d, want %d", row, top)
	}
	// And the column too: the composer counts from its own left edge, while the
	// screen has a border and its air in front of it. An empty prompt puts the
	// caret right after the prompt string, which is the one position this test
	// can state without restating the wrap arithmetic.
	if want := m.cfg.Layout.composerLeft(m.width) + lipgloss.Width(m.prompt()); column != want {
		t.Fatalf("caret column = %d, want %d inside the frame", column, want)
	}
}

// The palette belongs to the input area, so the gap stays above it: a blank row
// between the list of commands and the line being completed would separate the
// wrong two things.
func TestTheGapStaysAboveThePalette(t *testing.T) {
	m := answered(t)
	updated, _ := m.Update(typeText("/"))
	m = updated.(Model)
	if len(m.suggestionLines()) == 0 {
		t.Fatal("no palette to place the gap against")
	}

	frame := strings.Split(plain(m.View().Content), "\n")
	if got := strings.TrimSpace(frame[m.inputTop()-1]); got != "" {
		t.Fatalf("row %d = %q, want the blank gap above the palette", m.inputTop()-1, got)
	}
	if got := strings.TrimSpace(frame[m.bodyHeight()-1]); got == "" {
		t.Fatalf("row %d is blank, want the last row of the transcript", m.bodyHeight()-1)
	}
	for row := m.bodyHeight(); row < m.inputTop(); row++ {
		if got := strings.TrimSpace(frame[row]); got != "" {
			t.Fatalf("row %d = %q, want the blank gap above the palette", row, got)
		}
	}
	if got := frame[m.inputTop()]; !strings.Contains(got, "/model") {
		t.Fatalf("row %d = %q, want the palette to start at the input area", m.inputTop(), got)
	}
	if got := strings.Count(m.View().Content, "\n") + 1; got != m.height {
		t.Fatalf("the gap pushed the frame past the screen: %d rows, want %d", got, m.height)
	}
}

// The question offers a third answer, and the box says what it covers. "always"
// on its own would be a promise nobody could read back: which tool, and until
// when, are the two halves of the scope and both have to be on screen before the
// key that spends it.
func TestTheQuestionNamesWhatTheStandingAnswerCovers(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(AskMsg{
		Request: AskRequest{Subject: "Ran  bash  rm -rf build", Tool: "bash"},
		Reply:   make(chan AskAnswer, 1),
	})
	m = updated.(Model)

	frame := plain(m.View().Content)
	for _, want := range []string{"y yes", "n no", "a bash for this session"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the question is missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "always") {
		t.Fatalf("the box promised more than it can give:\n%s", frame)
	}
}

// A question that does not name a tool cannot be answered for the session, so it
// advertises two keys and the third one does nothing. A key that quietly meant
// yes would teach the reader to press it for a scope nobody defined.
func TestAQuestionWithoutAToolOffersOnlyTwoKeys(t *testing.T) {
	question := make(chan AskAnswer, 1)
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(AskMsg{Request: AskRequest{Subject: "something?"}, Reply: question})
	m = updated.(Model)
	if frame := plain(m.View().Content); strings.Contains(frame, "for this session") {
		t.Fatalf("a tool-less question offered a session answer:\n%s", frame)
	}

	updated, _ = m.Update(typeText("a"))
	m = updated.(Model)
	if m.approval == nil {
		t.Fatal("a key dismissed a question that has no third answer")
	}
	select {
	case got := <-question:
		t.Fatalf("the question was answered: %+v", got)
	default:
	}
}

// The third key answers yes and asks for the rest of the session in one go. It
// is one answer, not two: the call it is standing in front of still has to run.
func TestTheThirdKeyAnswersYesAndRemembers(t *testing.T) {
	question := make(chan AskAnswer, 1)
	m := running(t, Config{Styles: testStyles(t)})
	updated, _ := m.Update(AskMsg{
		Request: AskRequest{Subject: "Ran  write  a.txt", Tool: "write"},
		Reply:   question,
	})
	m = updated.(Model)

	updated, _ = m.Update(typeText("a"))
	m = updated.(Model)
	if m.approval != nil {
		t.Fatal("the box stayed up after the third answer")
	}
	got := <-question
	if !got.Allowed || !got.Always {
		t.Fatalf("third key answered %+v, want yes and standing", got)
	}
	if m.cancel == nil || !m.busy {
		t.Fatal("answering the question stopped the turn")
	}
}

// The busy marker is the one field of the footer this package owns, and it lives
// exactly as long as the turn: one frame per tick while something runs, nothing
// at all while nothing does. A tick that outlived its turn would keep repainting
// an idle screen, and one that counted towards the next turn would make the new
// turn start at the old one's age.
func TestTheBusyMarkerCountsOnlyWhileTheTurnRuns(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t)})
	if got, want := m.busyField(80), spinnerFrames[0]+" working"; got != want {
		t.Fatalf("busy field = %q, want %q before the first second", got, want)
	}
	if last := lastRow(m); !strings.HasSuffix(strings.TrimRight(last, " "), spinnerFrames[0]+" working") {
		t.Fatalf("status row = %q, want the mark and its label at the end", last)
	}

	turn := m.busySeq
	updated, cmd := m.Update(busyTickMsg{turn: turn})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("the tick did not arm the next one")
	}
	if got, want := m.busyField(80), spinnerFrames[1]+" working"; got != want {
		t.Fatalf("after one tick busy field = %q, want %q", got, want)
	}

	// The clock is whole seconds and starts at the first one: ten frames make a
	// second, and "0s" is a number about nothing.
	for i := 1; i < spinnerTicksPerSecond; i++ {
		updated, _ = m.Update(busyTickMsg{turn: turn})
		m = updated.(Model)
	}
	if got, want := m.busyField(80), spinnerFrames[0]+" working 1s"; got != want {
		t.Fatalf("after one second busy field = %q, want %q", got, want)
	}

	// A tick that belongs to a turn that is no longer the current one ends
	// there: counting it would both age the new turn and start a second chain.
	updated, stale := m.Update(busyTickMsg{turn: turn - 1})
	m = updated.(Model)
	if stale != nil {
		t.Fatal("a stale tick armed another one")
	}
	if got, want := m.busyField(80), spinnerFrames[0]+" working 1s"; got != want {
		t.Fatalf("a stale tick changed the count: %q, want %q", got, want)
	}

	updated, _ = m.Update(TurnMsg{View: ViewMain})
	m = updated.(Model)
	if got := m.busyField(80); got != "" {
		t.Fatalf("busy field after the turn = %q, want none", got)
	}
	updated, cmd = m.Update(busyTickMsg{turn: turn})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("a tick outlived its turn and armed another")
	}
	if last := lastRow(m); strings.Contains(last, "working") {
		t.Fatalf("status row = %q, want no marker on an idle screen", last)
	}
}

// The mark rotates rather than counting: a still screen under a stalled model
// looks like a screen with nothing happening, which is the one thing the field
// exists to rule out. One full cycle lands back on the first frame, and the
// clock it carries is independent of where in the cycle it is.
func TestTheBusyMarkerTurnsThroughEveryFrame(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t)})
	turn := m.busySeq

	seen := make(map[string]bool, len(spinnerFrames))
	for i := 0; i < len(spinnerFrames); i++ {
		field := m.busyField(80)
		if want := spinnerFrames[i] + " working"; field != want {
			t.Fatalf("frame %d = %q, want %q", i, field, want)
		}
		seen[field] = true
		updated, _ := m.Update(busyTickMsg{turn: turn})
		m = updated.(Model)
	}
	if len(seen) != len(spinnerFrames) {
		t.Fatalf("a cycle showed %d distinct frames, want %d", len(seen), len(spinnerFrames))
	}
	if got, want := m.busyField(80), spinnerFrames[0]+" working 1s"; got != want {
		t.Fatalf("after a full cycle busy field = %q, want %q", got, want)
	}
}

// The mark is paid for out of the width the host's own fields get, so it gives
// way in a fixed order: the label first, then the clock, and the frame never.
// A footer with no mark on it while a turn runs is the one outcome not worth
// any width at all.
func TestTheBusyMarkerDropsTheLabelBeforeTheFrame(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t)})
	turn := m.busySeq
	for i := 0; i < spinnerTicksPerSecond; i++ {
		updated, _ := m.Update(busyTickMsg{turn: turn})
		m = updated.(Model)
	}
	frame := spinnerFrames[m.busyTicks%len(spinnerFrames)]
	cases := []struct {
		width int
		want  string
	}{
		{80, frame + " working 1s"},
		{36, frame + " 1s"},
		{24, frame},
	}
	for _, c := range cases {
		if got := m.busyField(c.width); got != c.want {
			t.Fatalf("busy field at %d cells = %q, want %q", c.width, got, c.want)
		}
	}
}

// The marker is paid for out of the footer's width rather than appended past it:
// the host shortens and drops its own fields to fit, and a marker added after
// that would be the first thing cut on a narrow terminal.
func TestTheBusyMarkerKeepsItsRoomOnANarrowScreen(t *testing.T) {
	m := running(t, Config{Styles: testStyles(t)})
	m.cfg.Status = func(width int) string {
		line := "deepseek/deepseek-chat | think high | ctx 188k left"
		if len(line) > width {
			return line[:width]
		}
		return line
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)
	for i := 0; i < spinnerTicksPerSecond; i++ {
		updated, _ = m.Update(busyTickMsg{turn: m.busySeq})
		m = updated.(Model)
	}

	last := lastRow(m)
	if !strings.Contains(last, "working 1s") {
		t.Fatalf("status row = %q, want the marker to survive a narrow screen", last)
	}
	if width := len([]rune(strings.TrimRight(last, " "))); width > 40 {
		t.Fatalf("status row is %d cells wide, want at most 40", width)
	}
}

// lastRow is the footer as the terminal sees it.
func lastRow(m Model) string {
	lines := strings.Split(plain(m.View().Content), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// suggestionNames is the labels a palette is showing, for failure messages.
func suggestionNames(picks []Suggestion) []string {
	names := make([]string, 0, len(picks))
	for _, pick := range picks {
		names = append(names, pick.Name)
	}
	return names
}

// files is the file provider: the same palette, one callback further. It offers
// the whole tree as far as the interface is concerned — the host decides what a
// reference looks like, and this one looks like "@" followed by a word.
func files(line string) []Suggestion {
	at := strings.LastIndexByte(line, '@')
	if at < 0 || strings.ContainsFunc(line[at+1:], func(r rune) bool { return r == ' ' }) {
		return nil
	}
	typed := line[at+1:]
	token := len([]rune(line)) - len([]rune(line[:at]))
	out := make([]Suggestion, 0, 2)
	for _, pick := range []Suggestion{
		{Name: "@main.go", About: "1B"},
		{Name: "@mod.go", About: "1B"},
		{Name: "@internal/", About: "directory"},
	} {
		if strings.HasPrefix(pick.Name, "@"+typed) {
			pick.Replace = token
			out = append(out, pick)
		}
	}
	return out
}

// TestTabFillsAFileReferenceInPlace: the palette is the same list as the command
// palette, but a reference is one word of a sentence, so filling it in must not
// touch the words around it.
func TestTabFillsAFileReferenceInPlace(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Attach: files}))
	updated, _ := m.Update(typeText("explain @ma"))
	m = updated.(Model)

	if len(m.picks) != 1 || m.picks[0].Name != "@main.go" {
		t.Fatalf("picks = %v, want the one file", suggestionNames(m.picks))
	}
	updated, _ = m.Update(press(tea.KeyTab, 0))
	m = updated.(Model)
	if got, want := m.composer().String(), "explain @main.go"; got != want {
		t.Fatalf("tab filled %q, want %q", got, want)
	}
}

// TestEnterFillsAReferenceWithoutSendingIt: Enter finishes the word, and a word
// is not a sentence. The second press sends, because by then there is nothing
// left to fill.
func TestEnterFillsAReferenceWithoutSendingIt(t *testing.T) {
	sent := make(chan string, 2)
	runner := func(ctx context.Context, line string, sink Sink) error {
		sent <- line
		return nil
	}
	m := sized(t, New(Config{Styles: testStyles(t), Attach: files, Run: runner}))
	updated, _ := m.Update(typeText("explain @ma"))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	if got, want := m.composer().String(), "explain @main.go"; got != want {
		t.Fatalf("Enter left %q, want the reference filled", got)
	}
	select {
	case line := <-sent:
		t.Fatalf("Enter sent %q, want it to spend itself on the word", line)
	case <-time.After(50 * time.Millisecond):
	}

	updated, _ = m.Update(press(tea.KeyEnter, 0))
	select {
	case line := <-sent:
		if line != "explain @main.go" {
			t.Fatalf("sent %q, want the completed line", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Enter never sent")
	}
}

// TestTheFileProviderGetsTheFirstLook: while a reference is being typed the
// command table has nothing to say, and a host that reports candidates here is
// saying so. A line with no reference falls back to the commands.
func TestTheFileProviderGetsTheFirstLook(t *testing.T) {
	m := sized(t, New(Config{Styles: testStyles(t), Suggest: palette, Attach: files}))

	updated, _ := m.Update(typeText("explain @ma"))
	m = updated.(Model)
	if !m.attach || len(m.picks) != 1 || m.picks[0].Name != "@main.go" {
		t.Fatalf("picks = %v attach = %v, want the file and the flag", suggestionNames(m.picks), m.attach)
	}

	updated, _ = m.Update(press(tea.KeyEscape, 0))
	m = updated.(Model)
	for range "explain @ma" {
		updated, _ = m.Update(press(tea.KeyBackspace, 0))
		m = updated.(Model)
	}
	updated, _ = m.Update(typeText("/mo"))
	m = updated.(Model)
	if m.attach {
		t.Fatal("a command line was reported as an attachment")
	}
	if len(m.picks) != 1 || m.picks[0].Name != "/model" {
		t.Fatalf("picks = %v, want the command table's answer", suggestionNames(m.picks))
	}
}

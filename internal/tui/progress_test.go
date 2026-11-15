package tui

// Progress from a running tool, which is where the kernel's long-task work
// reaches the screen. Three promises are pinned here: the tail replaces itself
// rather than growing a line per notification, it does not repaint faster than
// a number can be read, and the outcome takes the slot back when the call
// returns.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pi-go/internal/wire"
)

// progressEvent builds the event the kernel emits for one tool.Partial: the
// same content-block encoding a finished result uses.
func progressEvent(t *testing.T, id, text string) wire.AgentEvent {
	t.Helper()
	raw, err := json.Marshal(struct {
		Content []wire.Content `json:"content"`
	}{Content: []wire.Content{wire.TextContent(text)}})
	if err != nil {
		t.Fatalf("marshal progress: %v", err)
	}
	return wire.AgentEvent{
		Type:          wire.AgentEventToolExecUpdate,
		ToolCallID:    id,
		ToolName:      "bash",
		PartialResult: raw,
	}
}

func runTool(t *testing.T, tr *Transcript, id, name string) {
	t.Helper()
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: id, ToolName: name})
}

func TestToolProgressReplacesItsOwnTail(t *testing.T) {
	tr := NewTranscript()
	now := time.Unix(1000, 0)
	tr.Now = func() time.Time { return now }

	runTool(t, &tr, "a", "bash")
	tr.Apply(progressEvent(t, "a", "3/10"))

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want one: a report is not a line of its own", len(blocks))
	}
	if blocks[0].Progress != "3/10" {
		t.Fatalf("progress = %q, want 3/10", blocks[0].Progress)
	}
	body := strings.Join(tr.Lines(80, testStyles(t), DefaultLayout()), "\n")
	if !strings.Contains(body, "3/10") {
		t.Fatalf("rendered transcript has no progress:\n%s", body)
	}

	now = now.Add(progressEvery)
	tr.Apply(progressEvent(t, "a", "4/10"))
	if got := tr.Blocks()[0].Progress; got != "4/10" {
		t.Fatalf("progress = %q, want the newer 4/10", got)
	}
	body = strings.Join(tr.Lines(80, testStyles(t), DefaultLayout()), "\n")
	if strings.Contains(body, "3/10") {
		t.Fatalf("the replaced count is still on screen:\n%s", body)
	}
}

func TestToolProgressDoesNotRepaintFasterThanItCanBeRead(t *testing.T) {
	tr := NewTranscript()
	now := time.Unix(1000, 0)
	tr.Now = func() time.Time { return now }

	runTool(t, &tr, "a", "bash")
	tr.Apply(progressEvent(t, "a", "1/60"))

	now = now.Add(progressEvery - 50*time.Millisecond)
	tr.Apply(progressEvent(t, "a", "2/60"))
	if got := tr.Blocks()[0].Progress; got != "1/60" {
		t.Fatalf("progress = %q, want the report inside the quiet window dropped", got)
	}

	now = now.Add(50 * time.Millisecond)
	tr.Apply(progressEvent(t, "a", "3/60"))
	if got := tr.Blocks()[0].Progress; got != "3/60" {
		t.Fatalf("progress = %q, want the report after the window taken", got)
	}
}

func TestToolProgressGivesTheSlotBackToTheOutcome(t *testing.T) {
	tr := NewTranscript()
	tr.Now = func() time.Time { return time.Unix(1000, 0) }

	runTool(t, &tr, "a", "bash")
	tr.Apply(progressEvent(t, "a", "7/10"))
	tr.Apply(wire.AgentEvent{
		Type:       wire.AgentEventToolExecEnd,
		ToolCallID: "a",
		ToolName:   "bash",
		Result:     []byte(`{"content":[{"type":"text","text":"one\ntwo\n"}]}`),
	})

	block := tr.Blocks()[0]
	if block.Progress != "" {
		t.Fatalf("progress = %q, want it cleared once the result landed", block.Progress)
	}
	if block.Note != "2 output lines" {
		t.Fatalf("note = %q, want the outcome alone", block.Note)
	}
	body := strings.Join(tr.Lines(80, testStyles(t), DefaultLayout()), "\n")
	if strings.Contains(body, "7/10") {
		t.Fatalf("a count for a call that already answered is still on screen:\n%s", body)
	}
}

func TestToolProgressForACallWithNoLineIsDropped(t *testing.T) {
	tr := NewTranscript()

	tr.Apply(progressEvent(t, "ghost", "3/10"))
	if tr.Len() != 0 {
		t.Fatalf("got %d blocks, want none: a resumed session replays messages, not calls", tr.Len())
	}

	runTool(t, &tr, "a", "bash")
	tr.Apply(wire.AgentEvent{Type: wire.AgentEventToolExecEnd, ToolCallID: "a", ToolName: "bash"})
	tr.Apply(progressEvent(t, "a", "9/10"))
	if got := tr.Blocks()[0].Progress; got != "" {
		t.Fatalf("progress = %q, want a report for a closed call dropped", got)
	}

	// An update without an id matches nothing, which is the state a server that
	// ignores progress tokens puts the client in.
	tr.Apply(progressEvent(t, "", "5/10"))
	if got := tr.Blocks()[0].Progress; got != "" {
		t.Fatalf("progress = %q, want a report without a call id dropped", got)
	}
}

func TestToolProgressFoldsAMultiLineMessageToOneLine(t *testing.T) {
	tr := NewTranscript()
	tr.Now = func() time.Time { return time.Unix(1000, 0) }

	runTool(t, &tr, "a", "bash")
	tr.Apply(progressEvent(t, "a", "line one\nline two\n"+strings.Repeat("x", 200)))

	got := tr.Blocks()[0].Progress
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("progress = %q, want the newlines folded", got)
	}
	if width := len([]rune(got)); width > progressCells {
		t.Fatalf("progress is %d runes, want at most %d", width, progressCells)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("progress = %q, want the cut marked", got)
	}
}

func TestToolProgressFollowsTheCallIDInsideABox(t *testing.T) {
	tr := NewTranscript()
	tr.Now = func() time.Time { return time.Unix(1000, 0) }

	// Parallel calls: the second starting closes the first as running, which is
	// the state the update must not write over.
	runTool(t, &tr, "a", "bash")
	runTool(t, &tr, "b", "read")
	tr.Apply(progressEvent(t, "b", "5/9"))

	blocks := tr.Blocks()
	if blocks[0].Progress != "" || !strings.Contains(blocks[0].Note, "running") {
		t.Fatalf("first call = %+v, want it closed as running", blocks[0])
	}
	if blocks[1].Progress != "5/9" {
		t.Fatalf("second call progress = %q, want 5/9", blocks[1].Progress)
	}

	seen := 0
	for _, line := range tr.Lines(80, testStyles(t), DefaultLayout()) {
		if !strings.Contains(line, "5/9") {
			continue
		}
		seen++
		if !strings.Contains(line, "read") {
			t.Fatalf("progress landed on the wrong call: %q", line)
		}
	}
	if seen != 1 {
		t.Fatalf("progress appears on %d lines, want exactly one", seen)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/cost"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// scriptRenderer hands back a renderer whose clock advances one fixed step per
// reading, so "Thought for 6.4s" is a fact the test sets rather than a race.
func scriptRenderer(step time.Duration) (*renderer, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	render := newRenderer(out, errOut, false)
	clock := time.Unix(0, 0)
	render.now = func() time.Time {
		clock = clock.Add(step)
		return clock
	}
	return render, out, errOut
}

func feed(t *testing.T, render *renderer, events ...wire.AgentEvent) {
	t.Helper()
	for _, event := range events {
		if err := render.event(event); err != nil {
			t.Fatalf("event %s: %v", event.Type, err)
		}
	}
}

func assistantStart() wire.AgentEvent {
	return wire.AgentEvent{Type: wire.AgentEventMessageStart, Message: &wire.Message{Role: wire.RoleAssistant}}
}

func delta(kind, text string) wire.AgentEvent {
	return wire.AgentEvent{Type: wire.AgentEventMessageUpdate, AssistantEvent: &wire.AssistantEvent{Type: kind, Delta: text}}
}

func stream(events ...string) []wire.AgentEvent {
	out := make([]wire.AgentEvent, 0, len(events))
	for _, text := range events {
		out = append(out, delta(wire.EventTextDelta, text))
	}
	return out
}

func assistantEnd(text string, reason wire.StopReason) wire.AgentEvent {
	return wire.AgentEvent{Type: wire.AgentEventMessageEnd, Message: &wire.Message{
		Role:       wire.RoleAssistant,
		Content:    wire.NewBlocks(wire.TextContent(text)),
		StopReason: reason,
	}}
}

func toolStart(id, name, args string) wire.AgentEvent {
	return wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolCallID: id, ToolName: name, Args: []byte(args)}
}

func toolEnd(id, name string, failed bool, result string, millis int64) wire.AgentEvent {
	// The payload is the kernel's own encoding of tool.Result, so it is built
	// rather than spelled out: a newline in the text has to stay escaped.
	payload, err := json.Marshal(struct {
		Content []wire.Content `json:"content"`
		IsError bool           `json:"isError"`
	}{Content: []wire.Content{wire.TextContent(result)}, IsError: failed})
	if err != nil {
		panic(err)
	}
	event := wire.AgentEvent{
		Type:       wire.AgentEventToolExecEnd,
		ToolCallID: id,
		ToolName:   name,
		Result:     payload,
		IsError:    wire.Ptr(failed),
	}
	if millis > 0 {
		event.DurationMs = wire.Ptr(millis)
	}
	return event
}

// TestRendererPinsTheTurnShape is the contract the terminal reads as: the answer
// under a solid bullet, everything the agent did under a branch two columns in,
// and a blank line between the two so neither reads as a continuation.
func TestRendererPinsTheTurnShape(t *testing.T) {
	render, out, _ := scriptRenderer(6400 * time.Millisecond)

	events := []wire.AgentEvent{
		assistantStart(),
		delta(wire.EventThinkingStart, ""),
		delta(wire.EventThinkingEnd, ""),
	}
	events = append(events, stream("I'll look.\nThen answer.")...)
	events = append(events,
		assistantEnd("", wire.StopToolUse),
		toolStart("call_1", "echo", `{"text":"hi"}`),
		// Under a second the duration is noise, so it stays off the line.
		toolEnd("call_1", "echo", false, "hi\n", 0),
		// What the kernel sends for the result message itself.
		wire.AgentEvent{Type: wire.AgentEventMessageStart, Message: &wire.Message{Role: wire.RoleToolResult}},
		wire.AgentEvent{Type: wire.AgentEventMessageEnd, Message: &wire.Message{Role: wire.RoleToolResult}},
		assistantStart(),
	)
	events = append(events, stream("done")...)
	events = append(events,
		assistantEnd("", wire.StopStop),
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)
	feed(t, render, events...)

	want := "  ├ • Thought for 6.4s\n" +
		"\n● I'll look.\n  Then answer.\n" +
		"\n  ├ • Ran  echo  {\"text\":\"hi\"} · 1 output line\n" +
		"\n● done\n"
	if got := out.String(); got != want {
		t.Fatalf("render mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestRendererClosesAnInFlightBranchWhenAnotherStarts pins the parallel case:
// both calls are running, so both lines say so, and each result lands on a line
// of its own once the branch it opened has already been closed. This is why
// every line uses the same connector — the renderer cannot know which one will
// turn out to be last.
func TestRendererClosesAnInFlightBranchWhenAnotherStarts(t *testing.T) {
	render, out, _ := scriptRenderer(time.Millisecond)

	feed(t, render,
		assistantStart(),
		assistantEnd("", wire.StopToolUse),
		toolStart("call_1", "grep", `{"pattern":"x"}`),
		toolStart("call_2", "read", `{"path":"a"}`),
		toolEnd("call_1", "grep", false, "one\ntwo", 0),
		toolEnd("call_2", "read", false, "a\nb\nc", 1200),
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)

	want := "  ├ • Ran  grep  {\"pattern\":\"x\"} · running\n" +
		"  ├ • Ran  read  {\"path\":\"a\"} · running\n" +
		"  ├ • Ran  grep · 2 output lines\n" +
		"  ├ • Ran  read · 3 output lines · 1.2s\n"
	if got := out.String(); got != want {
		t.Fatalf("render mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestRendererShowsAFailureBody: the model sees the error either way, but the
// command that broke is the human's to fix, so the one result worth printing
// unbidden is the one that failed.
func TestRendererShowsAFailureBody(t *testing.T) {
	render, out, _ := scriptRenderer(time.Millisecond)

	feed(t, render,
		assistantStart(),
		assistantEnd("", wire.StopToolUse),
		toolStart("call_1", "bash", `{"command":"exit 1"}`),
		toolEnd("call_1", "bash", true, "permission denied", 0),
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)

	want := "  ├ • Ran  bash  {\"command\":\"exit 1\"} · failed\n" +
		"    permission denied\n"
	if got := out.String(); got != want {
		t.Fatalf("render mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestRendererShowsAnAnswerWithoutDeltas: a provider that sends no streamed
// text still has its message, and dropping it would leave a prompt with nothing
// under it.
func TestRendererShowsAnAnswerWithoutDeltas(t *testing.T) {
	render, out, _ := scriptRenderer(time.Millisecond)

	feed(t, render,
		assistantStart(),
		assistantEnd("plain answer", wire.StopStop),
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)

	if got, want := out.String(), "● plain answer\n"; got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}

// TestRendererEndsAnAbandonedBranch: a cancelled turn never sends the result,
// so the branch is closed with what actually happened rather than left open for
// the stderr summary to land on.
func TestRendererEndsAnAbandonedBranch(t *testing.T) {
	render, out, _ := scriptRenderer(time.Millisecond)

	feed(t, render,
		assistantStart(),
		assistantEnd("", wire.StopToolUse),
		toolStart("call_1", "bash", `{"command":"sleep 100"}`),
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)

	want := "  ├ • Ran  bash  {\"command\":\"sleep 100\"} · interrupted\n"
	if got := out.String(); got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}

// TestRendererCompactionLines: compaction is one more thing the agent did, so
// it hangs off the same trunk and names what it cost. An overflow failure keeps
// its own wording, because its message already says what happened and what to do.
func TestRendererCompactionLines(t *testing.T) {
	render, out, _ := scriptRenderer(time.Millisecond)

	feed(t, render,
		wire.AgentEvent{Type: wire.AgentEventCompactionStart, Reason: wire.CompactionReasonManual},
		wire.AgentEvent{Type: wire.AgentEventCompactionEnd, Result: []byte(`{"tokensBefore":12000,"estimatedTokensAfter":3000}`)},
		wire.AgentEvent{Type: wire.AgentEventCompactionStart, Reason: wire.CompactionReasonThreshold},
		wire.AgentEvent{Type: wire.AgentEventCompactionEnd, ErrorMessage: "rate limited"},
		wire.AgentEvent{Type: wire.AgentEventCompactionStart, Reason: wire.CompactionReasonOverflow},
		wire.AgentEvent{
			Type:         wire.AgentEventCompactionEnd,
			Reason:       wire.CompactionReasonOverflow,
			ErrorMessage: "Context overflow: 900000 estimated tokens do not fit a 200000-token window.",
		},
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)

	want := "  ├ • Compacting  manual\n" +
		"  ├ • Compacted  12000 → 3000 tokens\n" +
		"  ├ • Compacting  threshold\n" +
		"  ├ • Compaction failed: rate limited\n" +
		"  ├ • Compacting  overflow\n" +
		"  ├ • Context overflow: 900000 estimated tokens do not fit a 200000-token window.\n"
	if got := out.String(); got != want {
		t.Fatalf("render mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestJSONModeIsUntouched: the protocol shape is the compatibility contract, so
// none of the tree reaches it.
func TestJSONModeIsUntouched(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	render := newRenderer(out, errOut, true)

	feed(t, render,
		delta(wire.EventTextDelta, "hello"),
		toolStart("call_1", "echo", `{"text":"hi"}`),
		toolEnd("call_1", "echo", false, "hi", 0),
		wire.AgentEvent{Type: wire.AgentEventEnd},
	)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("json mode wrote %d lines, want one per event: %q", len(lines), out.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") || strings.Contains(line, textMarker) {
			t.Fatalf("json line = %q, want the raw event", line)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing", errOut.String())
	}
}

// TestTheTurnSummaryStaysOnStderr keeps stdout pipe-clean: it is the answer and
// the tree, and the cost of the turn is diagnostics.
func TestTheTurnSummaryStaysOnStderr(t *testing.T) {
	render, out, errOut := scriptRenderer(time.Millisecond)
	render.summary(app.Result{
		Usage:      wire.Usage{Input: 1000, Output: 234, TotalTokens: 1234},
		StopReason: wire.StopStop,
	}, cost.Cost{
		Took:  8400 * time.Millisecond,
		Wrote: 2 * time.Second,
		Ended: time.Date(2026, 10, 21, 15, 3, 11, 0, time.Local),
	})

	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", out.String())
	}
	want := "[1234 tokens (in 1000, out 234), 117 tok/s, took 8.4s, ended at 15:03:11, stop reason stop]\n"
	if errOut.String() != want {
		t.Fatalf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestTheSummaryOfATurnThatSpentNothingStaysOneWord: the cost hangs off the
// tokens, so a turn with none reports only what explains it. A demo run and a
// request rejected before it ever reached a model both land here.
func TestTheSummaryOfATurnThatSpentNothingStaysOneWord(t *testing.T) {
	render, _, errOut := scriptRenderer(time.Millisecond)
	render.summary(app.Result{StopReason: wire.StopError}, cost.Cost{
		Took:  340 * time.Millisecond,
		Ended: time.Date(2026, 10, 21, 15, 3, 11, 0, time.Local),
	})

	want := "[stop reason error]\n"
	if errOut.String() != want {
		t.Fatalf("stderr = %q, want %q", errOut.String(), want)
	}
}

func TestHumanDurationReadsTheWayAHumanWouldSayIt(t *testing.T) {
	cases := []struct {
		duration time.Duration
		want     string
	}{
		{0, "0ms"},
		{840 * time.Millisecond, "840ms"},
		{time.Second, "1.0s"},
		{6400 * time.Millisecond, "6.4s"},
		{12900 * time.Millisecond, "12.9s"},
		{time.Minute, "1m00s"},
		{65 * time.Second, "1m05s"},
	}
	for _, c := range cases {
		if got := humanDuration(c.duration); got != c.want {
			t.Fatalf("humanDuration(%v) = %q, want %q", c.duration, got, c.want)
		}
	}
}

func TestPluralLinesCountsWhatTheModelReadsBack(t *testing.T) {
	cases := []struct {
		count int
		want  string
	}{
		{0, "no output"},
		{1, "1 output line"},
		{6, "6 output lines"},
	}
	for _, c := range cases {
		if got := pluralLines(c.count); got != c.want {
			t.Fatalf("pluralLines(%d) = %q, want %q", c.count, got, c.want)
		}
	}
	// Blank lines are not output: a trailing newline from a command is not a
	// line the model reads as content.
	if got := lineCount([]string{"a\n\nb\n"}); got != 2 {
		t.Fatalf("lineCount = %d, want the blank line dropped", got)
	}
}

// TestSideConversationUsesAnOpenBullet: a side answer is drawn by the same code
// as a main answer, and only the bullet changes. Two filled bullets in one
// scrollback would leave the reader guessing which conversation a branch
// belonged to after /parent.
func TestSideConversationUsesAnOpenBullet(t *testing.T) {
	render, out, _ := scriptRenderer(0)
	render.setSide(true)
	feed(t, render,
		assistantStart(),
		delta(wire.EventTextDelta, "side answer"),
		assistantEnd("", wire.StopStop),
		toolStart("call_1", "echo", `{"text":"hi"}`),
		toolEnd("call_1", "echo", false, "hi\n", 0),
	)
	want := "○ side answer\n\n  ├ ○ Ran  echo  {\"text\":\"hi\"} · 1 output line\n"
	if got := out.String(); got != want {
		t.Fatalf("side render mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}

	render.setSide(false)
	feed(t, render, assistantStart(), delta(wire.EventTextDelta, "back"), assistantEnd("", wire.StopStop))
	want += "\n● back\n"
	if got := out.String(); got != want {
		t.Fatalf("returning to the main bullet\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// approvalCall is one call as the question sees it.
func approvalCall(id, name, args string) tool.Call {
	return tool.Call{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

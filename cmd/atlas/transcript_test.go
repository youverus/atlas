package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// A replayed conversation is what the full-screen session shows after a switch,
// so its shape is a contract: prompts under "> ", answers under the bar the
// interface draws down their left edge, one branch per tool call carrying what it
// produced, and the body only of a failed one. Thinking is left out: a live turn
// reports only how long it took, and that duration is not in the session file.
func TestWriteTranscriptPinsTheShapes(t *testing.T) {
	messages := []wire.Message{
		wire.SystemMessage("you are an agent"),
		wire.UserMessage("hello\nthere"),
		{
			Role: wire.RoleAssistant,
			Content: wire.NewBlocks(
				wire.ThinkingContent("think twice", ""),
				wire.TextContent("answer"),
				wire.ToolCallContent("call_1", "bash", json.RawMessage(`{"command":"ls"}`)),
			),
			StopReason: wire.StopToolUse,
		},
		wire.ToolResultMessage("call_1", "bash", false, wire.TextContent("a.txt")),
		wire.ToolResultMessage("call_2", "read", true, wire.TextContent("permission denied")),
		{
			Role:         wire.RoleAssistant,
			Content:      wire.NewBlocks(wire.TextContent("done")),
			StopReason:   wire.StopError,
			ErrorMessage: "rate limited",
		},
	}

	out := &bytes.Buffer{}
	if !writeTranscript(out, messages, nil) {
		t.Fatal("writeTranscript reported nothing for a five-message conversation")
	}
	// The result of call_1 folds into the branch that produced it; call_2 has
	// no call in the conversation, so its result keeps a line of its own.
	want := "\nhistory: 5 messages\n" +
		"\n> hello\n  there\n" +
		"\n│ answer\n\n  ├ • Ran  bash  {\"command\":\"ls\"} · 1 output line\n" +
		"\n  ├ • Ran  read · failed\n    permission denied\n" +
		"\n│ done\nerror: rate limited\n" +
		"\n"
	if got := out.String(); got != want {
		t.Fatalf("transcript mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// A call whose result never reached the file still replays: a cancelled turn
// records the call and nothing after it, and a line that looked complete would
// claim it ran.
func TestWriteTranscriptMarksACallWithNoResult(t *testing.T) {
	messages := []wire.Message{
		wire.UserMessage("go"),
		{
			Role:       wire.RoleAssistant,
			Content:    wire.NewBlocks(wire.ToolCallContent("call_1", "bash", json.RawMessage(`{"command":"sleep 100"}`))),
			StopReason: wire.StopToolUse,
		},
	}
	out := &bytes.Buffer{}
	if !writeTranscript(out, messages, nil) {
		t.Fatal("writeTranscript printed nothing for an interrupted turn")
	}
	want := "\n  ├ • Ran  bash  {\"command\":\"sleep 100\"} · no result\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("transcript = %q, want %q", out.String(), want)
	}
}

// A wrapped answer carries the bar on every line: a bullet and an indent on the
// first line only would stop reading as one block halfway down the screen.
func TestWriteTranscriptDrawsTheBarOnEveryAnswerLine(t *testing.T) {
	out := &bytes.Buffer{}
	messages := []wire.Message{
		wire.UserMessage("hello"),
		wire.AssistantMessage(wire.TextContent("first line\nsecond line")),
	}
	if !writeTranscript(out, messages, nil) {
		t.Fatal("writeTranscript printed nothing for a replayed answer")
	}
	want := "\n│ first line\n│ second line\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("transcript = %q, want %q", out.String(), want)
	}
}

// A session with no conversation is quiet: a fresh session, or one that
// recorded nothing but its system prompt, has no history to replay, and a
// "history: 0 messages" header would be noise on every start.
func TestWriteTranscriptStaysQuietWithoutAConversation(t *testing.T) {
	out := &bytes.Buffer{}
	if writeTranscript(out, []wire.Message{wire.SystemMessage("sys")}, nil) {
		t.Fatal("writeTranscript printed for a system-only session")
	}
	if writeTranscript(out, nil, nil) {
		t.Fatal("writeTranscript printed for no messages")
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing", out.String())
	}
}

// A prompt that was only an image still happened: dropping it would leave the
// answer below it looking unprompted.
func TestWriteTranscriptKeepsAnImagePrompt(t *testing.T) {
	out := &bytes.Buffer{}
	messages := []wire.Message{
		wire.UserMessageBlocks(wire.ImageContent("iVBORw0KGgo=", "image/png")),
		wire.AssistantMessage(wire.TextContent("that is the login screen")),
	}
	if !writeTranscript(out, messages, nil) {
		t.Fatal("writeTranscript printed nothing for an image prompt")
	}
	for _, want := range []string{"\n> [image: image/png]\n", "\n│ that is the login screen\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("transcript = %q, want %q", out.String(), want)
		}
	}
}

// The switch reports what it did and nothing else: the conversation itself is
// drawn by whoever owns the screen, and printing it here too is what made a
// resumed turn appear twice on one screen.
func TestResumeReportsTheSwitchWithoutReplayingIt(t *testing.T) {
	application, _, target := resumeSetup(t)

	out, err := runCommand(t, application, "/resume "+target.ID()[:8])
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	for _, want := range []string{
		"resumed: alpha",
		"file: " + target.File(),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
	for _, unwanted := range []string{"history:", "> hello", "│ answer"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output = %q, want no replay (%q)", out, unwanted)
		}
	}
	// The messages are restored even though they are not printed: the next turn
	// and the next redraw read them from here.
	if len(application.Messages()) != 2 {
		t.Fatalf("restored %d messages, want 2", len(application.Messages()))
	}
}

// Resuming an empty session adds no history block: there is nothing in it to
// replay, and the switch is still worth reporting. The header-only file here is
// what an older atlas, or a write that died before its first entry, leaves
// behind; nothing creates one any more.
func TestResumeAnEmptySessionPrintsNoHistory(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	seeded := sessionAt(t, cwd, dir)
	seededApp := commandApp(t, seeded, cwd, faux.Response{Text: "answer"})
	if _, err := seededApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	empty := sessionAt(t, cwd, dir)
	if err := session.CreateFile(empty.File(), &session.File{Header: empty.Header()}); err != nil {
		t.Fatalf("write the empty session: %v", err)
	}

	out, err := runCommand(t, seededApp, "/resume "+empty.File())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(out, "resumed:") {
		t.Fatalf("output = %q, want the resumed line", out)
	}
	if strings.Contains(out, "history:") {
		t.Fatalf("output = %q, want no history block for an empty session", out)
	}
}

// An attached file is stored twice: the line the reader typed, then the file's
// contents under its path. Replaying a resumed session must show the line and
// not the paste — the answer belongs under a prompt, not under a file.
func TestWriteTranscriptShowsTheLineWithoutItsAttachmentBody(t *testing.T) {
	out := &bytes.Buffer{}
	tag := "<file name=\"/tmp/main.go\">\npackage main\n\nfunc main() {}\n</file>"
	messages := []wire.Message{
		wire.UserMessageBlocks(wire.TextContent("explain @main.go"), wire.TextContent(tag)),
		wire.AssistantMessage(wire.TextContent("it starts here")),
	}
	if !writeTranscript(out, messages, nil) {
		t.Fatal("writeTranscript printed nothing")
	}
	if !strings.Contains(out.String(), "> explain @main.go\n") {
		t.Fatalf("transcript = %q, want the typed line", out.String())
	}
	for _, unwanted := range []string{"<file name=", "func main()"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("transcript = %q, want no %q", out.String(), unwanted)
		}
	}
}

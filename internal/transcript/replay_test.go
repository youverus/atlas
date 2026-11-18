package transcript

import (
	"encoding/json"
	"testing"

	"pi-go/internal/wire"
)

// A replayed conversation has to read like the live one: a prompt, an answer,
// and one line per call with its outcome. A call and its result are two records
// in the file, so the fold is where they become one row.
func TestReplayFoldsCallsAndResultsIntoOneLine(t *testing.T) {
	messages := []wire.Message{
		wire.UserMessage("list the files"),
		{
			Role: wire.RoleAssistant,
			Content: wire.NewBlocks(
				wire.TextContent("checking"),
				wire.ToolCallContent("c1", "ls", json.RawMessage(`{"path":"."}`)),
			),
		},
		{
			Role:       wire.RoleToolResult,
			ToolCallID: "c1",
			ToolName:   "ls",
			Content:    wire.NewBlocks(wire.TextContent("a.go")),
		},
		{Role: wire.RoleAssistant, Content: wire.NewBlocks(wire.TextContent("two files"))},
	}

	blocks := Replay(messages)
	if len(blocks) != 4 {
		t.Fatalf("blocks = %d, want 4: %+v", len(blocks), blocks)
	}
	if blocks[0].Role != RoleUser || blocks[0].Text != "list the files" {
		t.Fatalf("prompt = %+v", blocks[0])
	}
	if blocks[1].Role != RoleAnswer || blocks[1].Text != "checking" {
		t.Fatalf("answer = %+v", blocks[1])
	}
	// The result is folded into the call, not drawn a second time.
	if blocks[2].Role != RoleTool || blocks[2].Name != "ls" || blocks[2].ID != "c1" {
		t.Fatalf("call = %+v", blocks[2])
	}
	if blocks[2].Note != `{"path":"."} · done` {
		t.Fatalf("note = %q, want the arguments and the outcome", blocks[2].Note)
	}
	// The card opens onto the whole call: the note is the preview, these are
	// what a reader clicks for.
	if blocks[2].Args != "{\n  \"path\": \".\"\n}" {
		t.Fatalf("args = %q, want the indented arguments", blocks[2].Args)
	}
	if blocks[2].Result != "a.go" {
		t.Fatalf("result = %q, want the output of a successful call", blocks[2].Result)
	}
	if blocks[2].Detail != "" {
		t.Fatalf("detail = %q, want the terminal's failure excerpt left empty", blocks[2].Detail)
	}
	if blocks[3].Role != RoleAnswer || blocks[3].Text != "two files" {
		t.Fatalf("final answer = %+v", blocks[3])
	}
}

// A thinking block is a live-only shape: the session file records the answer,
// and a replayed "thought for…" line would have no duration to print.
func TestReplaySkipsThinkingAndKeepsAFailure(t *testing.T) {
	failed := true
	messages := []wire.Message{
		{
			Role: wire.RoleAssistant,
			Content: wire.NewBlocks(
				wire.ThinkingContent("considering", ""),
				wire.ToolCallContent("c1", "read", json.RawMessage(`{"path":"missing"}`)),
			),
			StopReason:   wire.StopError,
			ErrorMessage: "provider timed out",
		},
		{
			Role:       wire.RoleToolResult,
			ToolCallID: "c1",
			ToolName:   "read",
			IsError:    &failed,
			Content:    wire.NewBlocks(wire.TextContent("no such file")),
		},
	}

	blocks := Replay(messages)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want the call and the error: %+v", len(blocks), blocks)
	}
	if blocks[0].Role != RoleTool || blocks[0].Note != `{"path":"missing"} · failed` {
		t.Fatalf("call = %+v", blocks[0])
	}
	if blocks[0].Detail != "no such file" {
		t.Fatalf("detail = %q, want the failure body", blocks[0].Detail)
	}
	if blocks[1].Role != RoleError || blocks[1].Text != "provider timed out" {
		t.Fatalf("error = %+v", blocks[1])
	}
}

// An attachment is stored twice: the line the reader typed, then the file's own
// text. Replaying both would print a whole file under the prompt that asked
// about it.
func TestReplayKeepsThePromptLineAndDropsTheAttachmentBody(t *testing.T) {
	message := wire.Message{
		Role: wire.RoleUser,
		Content: wire.NewBlocks(
			wire.TextContent("what is this?"),
			wire.TextContent("<file name=\"a.go\">\npackage a\n</file>"),
		),
	}
	blocks := Replay([]wire.Message{message})
	if len(blocks) != 1 || blocks[0].Text != "what is this?" {
		t.Fatalf("blocks = %+v, want only the typed line", blocks)
	}
}

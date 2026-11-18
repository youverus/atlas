package trajectory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// loadEntries reads a recorded session from testdata. The fixture is a real file
// rather than entries built in memory because the seq numbers and the raw lines
// this package reports only exist after a load.
func loadEntries(t *testing.T, name string) []session.Entry {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "trajectory", name)
	file, err := session.LoadFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return file.Entries
}

// strptr spells the pointers the fold exchanges: the leaf a caller is showing,
// and the parent link of a session that has one.
func strptr(value string) *string { return &value }

// normalize re-emits JSON through the types it is made of, so a golden file can
// be formatted for a reader without the comparison depending on whitespace.
func normalize(t *testing.T, raw []byte) []byte {
	t.Helper()
	var rows []Row
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	normalized, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("encode rows: %v", err)
	}
	return normalized
}

func TestFoldMatchesTheGolden(t *testing.T) {
	rows := Fold(loadEntries(t, "branch.jsonl"), strptr("e18"))
	got, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("encode rows: %v", err)
	}
	path := filepath.Join("..", "..", "testdata", "trajectory", "branch.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nfolded:\n%s", path, err, got)
	}
	// The golden is compared through the types it decodes to, so the file can be
	// laid out for a reader while this side stays the fold's own output.
	if !bytes.Equal(got, normalize(t, want)) {
		t.Fatalf("golden %s does not match\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

// A session file holds every branch ever taken. The fold answers for the leaf
// the window is showing, so the abandoned attempt must not appear at all: a row
// from another branch would describe work that never happened on this path.
func TestFoldKeepsOnlyTheBranchToTheLeaf(t *testing.T) {
	rows := Fold(loadEntries(t, "branch.jsonl"), strptr("e18"))
	if len(rows) != 16 {
		t.Fatalf("rows = %d, want 16", len(rows))
	}
	for _, row := range rows {
		if row.EntryID == "e13" || row.EntryID == "e14" {
			t.Fatalf("row %d kept abandoned entry %s", row.Index, row.EntryID)
		}
	}
}

// No leaf means nothing is on the branch. Returning the whole file instead would
// look like a complete answer while describing an arbitrary path.
func TestFoldWithoutALeafIsEmpty(t *testing.T) {
	if rows := Fold(loadEntries(t, "branch.jsonl"), nil); len(rows) != 0 {
		t.Fatalf("rows = %d, want none", len(rows))
	}
}

// The call and its result are two lines in the file but one event. The result
// lands on the call's row, which also takes the call's seq: the row is anchored
// where the model emitted it, not where the answer came back.
func TestFoldPairsEachResultWithItsCall(t *testing.T) {
	rows := Fold(loadEntries(t, "branch.jsonl"), strptr("e18"))
	var call *Row
	for i := range rows {
		if rows[i].Kind == KindTool && rows[i].Ref == "c1" {
			call = &rows[i]
			break
		}
	}
	if call == nil {
		t.Fatal("no row for call c1")
	}
	if call.Tool != "read" {
		t.Fatalf("tool = %q, want read", call.Tool)
	}
	if !strings.Contains(call.Args, `"path":"a.go"`) {
		t.Fatalf("args = %q, want the call's arguments", call.Args)
	}
	if !strings.Contains(call.Result, "permission denied") {
		t.Fatalf("result = %q, want the denial", call.Result)
	}
	if !call.IsError {
		t.Fatal("denied call is not marked as an error")
	}
	if call.DurationMs != 37 {
		t.Fatalf("duration = %d, want 37", call.DurationMs)
	}
	if call.Seq != 3 {
		t.Fatalf("seq = %d, want 3: the line the call was recorded on", call.Seq)
	}
	if call.Turn != 1 {
		t.Fatalf("turn = %d, want 1", call.Turn)
	}
}

// The transcript drops thinking, collapses a prompt to its text, and has no
// reading for compaction, model changes, labels or spend. Those are the facts a
// reader chasing a wrong turn is looking for, so the fold must keep them.
func TestFoldKeepsWhatTheTranscriptDrops(t *testing.T) {
	rows := Fold(loadEntries(t, "branch.jsonl"), strptr("e18"))
	byEntry := map[string]Row{}
	for _, row := range rows {
		if row.Kind != KindTool {
			byEntry[row.EntryID] = row
		}
	}

	// The attachment body travels under the prompt and is part of what the
	// model saw, so the prompt row keeps it.
	prompt := byEntry["e5"]
	if prompt.Kind != KindPrompt || !strings.Contains(prompt.Text, `<file name="b.go">`) {
		t.Fatalf("prompt row = %+v, want the attached body", prompt)
	}
	if prompt.Turn != 2 {
		t.Fatalf("prompt turn = %d, want 2", prompt.Turn)
	}

	// Reasoning is dropped by the transcript because a replayed block has no
	// duration to show; here the reasoning is the point.
	if round := byEntry["e2"]; !strings.Contains(round.Thinking, "stale") {
		t.Fatalf("round thinking = %q, want the recorded reasoning", round.Thinking)
	}

	// A compaction is the moment history was replaced, which is the first
	// question to ask about a constraint the model stopped honouring.
	compaction := byEntry["e6"]
	if compaction.Kind != KindCompaction || compaction.TokensBefore != 48000 {
		t.Fatalf("compaction row = %+v, want tokensBefore 48000", compaction)
	}
	if compaction.Ref != "e4" {
		t.Fatalf("compaction ref = %q, want the first kept entry e4", compaction.Ref)
	}

	// The result folded into the call's row, so it is not an entry of its own.
	if _, ok := byEntry["e3"]; ok {
		t.Fatal("the tool result became its own row")
	}

	// A line this package has no reading for keeps its raw JSON rather than
	// disappearing: the reader can still see what was recorded.
	custom := byEntry["e17"]
	if custom.Kind != KindOther || custom.Ref != "custom" || !strings.Contains(custom.Text, `"run-state"`) {
		t.Fatalf("custom row = %+v, want the raw line", custom)
	}

	// A role the kernel does not model is still something the model was handed.
	unknown := byEntry["e18"]
	if unknown.Kind != KindOther || unknown.Ref != "bashExecution" {
		t.Fatalf("unknown role row = %+v, want ref bashExecution", unknown)
	}
}

// The transcript caps a result at 20000 characters, which is the right call for
// a reading view and the wrong one for evidence: a reader chasing a failure
// needs the half that was cut. The fold keeps the whole output, so the ceiling
// stays a display decision rather than a fact the data lost.
func TestFoldKeepsALongResultWhole(t *testing.T) {
	payload := strings.Repeat("x", 50000)
	result, err := json.Marshal(wire.Message{
		Role:       wire.RoleToolResult,
		Content:    wire.NewTextList(payload),
		ToolCallID: "big",
		ToolName:   "read",
	})
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	entries := []session.Entry{
		{
			Type:      session.TypeMessage,
			ID:        "r1",
			Timestamp: "2026-11-18T13:00:00.000Z",
			Message:   json.RawMessage(`{"role":"user","content":"read the big file","timestamp":1}`),
		},
		{
			Type:      session.TypeMessage,
			ID:        "r2",
			ParentID:  strptr("r1"),
			Timestamp: "2026-11-18T13:00:01.000Z",
			Message:   json.RawMessage(`{"role":"assistant","content":[{"type":"toolCall","id":"big","name":"read","arguments":{"path":"big.go"}}],"timestamp":2}`),
		},
		{
			Type:      session.TypeMessage,
			ID:        "r3",
			ParentID:  strptr("r2"),
			Timestamp: "2026-11-18T13:00:02.000Z",
			Message:   result,
		},
	}

	rows := Fold(entries, strptr("r3"))
	var call *Row
	for i := range rows {
		if rows[i].Ref == "big" {
			call = &rows[i]
			break
		}
	}
	if call == nil {
		t.Fatalf("no row for call big in %d rows", len(rows))
	}
	if call.Result != payload {
		t.Fatalf("result is %d characters, want all %d", len(call.Result), len(payload))
	}
}

// A system message arrives either whole or as a patch naming what moved. A
// rewritten prompt is worth showing in full; a section or tool roster change is
// worth naming, because the identity of what changed is the fact.
func TestFoldDescribesASystemPatch(t *testing.T) {
	patch, err := json.Marshal(wire.Message{
		Role:       wire.RoleSystem,
		Content:    wire.NewTextList("fresh instructions"),
		Sections:   map[string]*string{"guidelines": wire.Ptr("be brief"), "legacy": nil},
		ToolsAdded: []wire.Tool{{Name: "grep"}, {Name: "edit"}},
	})
	if err != nil {
		t.Fatalf("encode patch: %v", err)
	}

	rows := Fold([]session.Entry{
		{
			Type:      session.TypeMessage,
			ID:        "s1",
			Timestamp: "2026-11-18T14:00:00.000Z",
			Message:   patch,
		},
	}, strptr("s1"))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	text := rows[0].Text
	// Sections are named in sorted order so an unchanged patch always renders
	// the same way.
	if !strings.Contains(text, "sections: +guidelines -legacy") {
		t.Fatalf("text = %q, want the section patch", text)
	}
	if !strings.Contains(text, "tools: +edit +grep") {
		t.Fatalf("text = %q, want the added tools in order", text)
	}
	if !strings.Contains(text, "fresh instructions") {
		t.Fatalf("text = %q, want the rewritten prompt", text)
	}
}

// A row for a line this package cannot read keeps the line. An entry that came
// from a file still has it; one assembled in memory does not, and its body is
// not always in the message field, so it has to be marshalled back rather than
// rendered as nothing.
func TestFoldRebuildsALineWithNoRecordedForm(t *testing.T) {
	entries := []session.Entry{
		{
			Type:       session.TypeCustomMessage,
			ID:         "c1",
			Timestamp:  "2026-11-18T15:00:00.000Z",
			CustomType: "note",
			Content:    json.RawMessage(`"a note"`),
		},
		{
			Type:      "something_new",
			ID:        "c2",
			ParentID:  strptr("c1"),
			Timestamp: "2026-11-18T15:00:01.000Z",
		},
	}

	rows := Fold(entries, strptr("c2"))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Kind != KindOther {
			t.Fatalf("%s kind = %s, want other", row.EntryID, row.Kind)
		}
		if !json.Valid([]byte(row.Text)) {
			t.Fatalf("%s text = %q, want the line as JSON", row.EntryID, row.Text)
		}
	}
	if !strings.Contains(rows[0].Text, `"a note"`) {
		t.Fatalf("text = %q, want the custom message's content", rows[0].Text)
	}
}

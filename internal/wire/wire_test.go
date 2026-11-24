package wire_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pi-go/internal/wire"
)

func TestSubagentRolesSkipQuotedFilePaths(t *testing.T) {
	for _, text := range []string{
		`look at @"notes @explorer draft.md" and ask @verifier`,
		`look at (@"notes \" @explorer draft.md") and ask @verifier`,
		`@verifier look at @"notes @explorer draft`,
	} {
		if got := wire.SubagentRoles(text); !reflect.DeepEqual(got, []string{wire.SubagentVerifier}) {
			t.Errorf("SubagentRoles(%q) = %v, want verifier", text, got)
		}
	}
}

func TestFixturesRoundTrip(t *testing.T) {
	paths, err := filepath.Glob("../../testdata/wire/*.json")
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var message wire.Message
			if err := json.Unmarshal(original, &message); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var want, got any
			if err := json.Unmarshal(original, &want); err != nil {
				t.Fatalf("decode original: %v", err)
			}
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("decode round trip: %v", err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip changed the message\nwant: %s\ngot:  %s", original, encoded)
			}
		})
	}
}

func TestPlainContentStaysAString(t *testing.T) {
	var message wire.Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"content":"hello"`) {
		t.Fatalf("expected the string form to survive, got %s", encoded)
	}
}

func TestBlockContentAcceptsArray(t *testing.T) {
	var message wire.Message
	raw := `{"role":"user","content":[{"type":"text","text":"a"},{"type":"image","data":"AA==","mimeType":"image/png"}]}`
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := message.Content.Len(); got != 2 {
		t.Fatalf("blocks: got %d, want 2", got)
	}
	if got := message.Text(); got != "a" {
		t.Fatalf("text: got %q", got)
	}
}

func TestEventKeepsZeroContentIndex(t *testing.T) {
	index := 0
	event := wire.AssistantEvent{Type: wire.EventTextDelta, ContentIndex: &index, Delta: "x"}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"contentIndex":0`) {
		t.Fatalf("contentIndex 0 was dropped: %s", encoded)
	}
}

func TestNormalizeContext(t *testing.T) {
	user := wire.UserMessage("hi")

	plain := wire.NormalizeContext("", nil, []wire.Message{user})
	if len(plain) != 1 || plain[0].Role != wire.RoleUser {
		t.Fatalf("expected no synthetic system message, got %+v", plain)
	}

	withPrompt := wire.NormalizeContext("sys", []wire.Tool{{Name: "echo"}}, []wire.Message{user})
	if len(withPrompt) != 2 {
		t.Fatalf("expected a leading system message, got %d messages", len(withPrompt))
	}
	if withPrompt[0].Role != wire.RoleSystem || withPrompt[0].Text() != "sys" {
		t.Fatalf("unexpected system message: %+v", withPrompt[0])
	}
	if len(withPrompt[0].ToolsAdded) != 1 || withPrompt[0].ToolsAdded[0].Name != "echo" {
		t.Fatalf("tools not declared: %+v", withPrompt[0].ToolsAdded)
	}

	existing := wire.NormalizeContext("sys", nil, []wire.Message{wire.SystemMessage("already"), user})
	if len(existing) != 2 || existing[0].Text() != "already" {
		t.Fatalf("existing system message was duplicated: %+v", existing)
	}
}

func TestCurrentToolsReplay(t *testing.T) {
	first := wire.SystemMessage("")
	first.ToolsAdded = []wire.Tool{{Name: "read"}, {Name: "write"}}
	second := wire.SystemMessage("")
	second.ToolsRemoved = []wire.ToolReference{{Name: "read"}}
	second.ToolsAdded = []wire.Tool{{Name: "bash"}}

	got := wire.CurrentTools([]wire.Message{first, second})
	var names []string
	for _, item := range got {
		names = append(names, item.Name)
	}
	want := []string{"write", "bash"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tools: got %v, want %v", names, want)
	}
}

func TestApplySystemSections(t *testing.T) {
	first := wire.SystemMessage("")
	first.Sections = map[string]*string{"rules": wire.Ptr("one"), "style": wire.Ptr("two")}
	second := wire.SystemMessage("")
	second.Sections = map[string]*string{"rules": nil, "extra": wire.Ptr("three")}

	got := wire.ApplySystemSections([]wire.Message{first, second})
	want := map[string]string{"style": "two", "extra": "three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sections: got %v, want %v", got, want)
	}
}

func TestToolResultIsErrorRoundTrip(t *testing.T) {
	result := wire.ToolResultMessage("call_1", "echo", true, wire.TextContent("boom"))
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"isError":true`) {
		t.Fatalf("isError missing: %s", encoded)
	}
	if !result.IsErrorResult() {
		t.Fatal("IsErrorResult should be true")
	}
}

func TestCurrentSystemMessageReplaysEverySystemMessage(t *testing.T) {
	if _, ok := wire.CurrentSystemMessage(nil); ok {
		t.Fatal("an empty transcript produced a system message")
	}

	first := wire.SystemMessage("base prompt")
	first.Timestamp = 1
	first.Sections = map[string]*string{"rules": wire.Ptr("one")}
	first.ToolsAdded = []wire.Tool{{Name: "read"}}

	second := wire.Message{Role: wire.RoleSystem, Content: wire.NewTextList("appended"), Timestamp: 2}
	second.Sections = map[string]*string{"rules": wire.Ptr("two"), "extra": wire.Ptr("three")}
	second.ToolsRemoved = []wire.ToolReference{{Name: "read"}}
	second.ToolsAdded = []wire.Tool{{Name: "bash"}}

	got, ok := wire.CurrentSystemMessage([]wire.Message{first, wire.UserMessage("hi"), second})
	if !ok {
		t.Fatal("a transcript with system messages produced none")
	}
	if text := got.Text(); text != "base prompt\n\nappended" {
		t.Fatalf("content = %q", text)
	}
	if got.Timestamp != 1 {
		t.Fatalf("timestamp = %d, want the first system message timestamp", got.Timestamp)
	}
	if got.Sections == nil || *got.Sections["rules"] != "two" || *got.Sections["extra"] != "three" || len(got.Sections) != 2 {
		t.Fatalf("sections = %v", got.Sections)
	}
	if len(got.ToolsAdded) != 1 || got.ToolsAdded[0].Name != "bash" {
		t.Fatalf("tools = %+v", got.ToolsAdded)
	}

	// Tool declarations alone are enough to produce a system message.
	toolsOnly := wire.SystemMessage("")
	toolsOnly.Timestamp = 5
	toolsOnly.ToolsAdded = []wire.Tool{{Name: "read"}}
	got, ok = wire.CurrentSystemMessage([]wire.Message{toolsOnly})
	if !ok || got.Timestamp != 5 || len(got.ToolsAdded) != 1 {
		t.Fatalf("tools-only system message = %+v, %v", got, ok)
	}
	if got.Sections != nil {
		t.Fatalf("sections = %v, want nil", got.Sections)
	}
}

// TestCompactionEventsMatchPiShape pins the two events pi's session layer adds on
// top of the agent lifecycle. The keys and their spelling are the protocol, so a
// rename has to fail here rather than in another client.
func TestCompactionEventsMatchPiShape(t *testing.T) {
	start, err := json.Marshal(wire.AgentEvent{
		Type:   wire.AgentEventCompactionStart,
		Reason: wire.CompactionReasonThreshold,
	})
	if err != nil {
		t.Fatalf("marshal start: %v", err)
	}
	if string(start) != `{"type":"compaction_start","reason":"threshold"}` {
		t.Fatalf("compaction_start = %s", start)
	}

	tokensAfter := 1200
	result, err := json.Marshal(wire.CompactionResult{
		Summary:              "checkpoint",
		FirstKeptEntryID:     "entry-b",
		TokensBefore:         4000,
		EstimatedTokensAfter: &tokensAfter,
		Usage:                &wire.Usage{TotalTokens: 80},
		Details:              json.RawMessage(`{"readFiles":["a.go"],"modifiedFiles":[]}`),
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	end, err := json.Marshal(wire.AgentEvent{
		Type:      wire.AgentEventCompactionEnd,
		Reason:    wire.CompactionReasonManual,
		Result:    json.RawMessage(result),
		Aborted:   wire.Ptr(false),
		WillRetry: wire.Ptr(true),
	})
	if err != nil {
		t.Fatalf("marshal end: %v", err)
	}
	var endFields map[string]json.RawMessage
	if err := json.Unmarshal(end, &endFields); err != nil {
		t.Fatalf("decode end: %v", err)
	}
	for _, key := range []string{"type", "reason", "result", "aborted", "willRetry"} {
		if _, ok := endFields[key]; !ok {
			t.Fatalf("compaction_end = %s, missing %q", end, key)
		}
	}
	for _, key := range []string{"message", "toolCallId", "args", "isError", "errorMessage"} {
		if _, ok := endFields[key]; ok {
			t.Fatalf("compaction_end = %s, unexpected %q", end, key)
		}
	}

	var resultFields map[string]json.RawMessage
	if err := json.Unmarshal(endFields["result"], &resultFields); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	want := map[string]string{
		"summary":              `"checkpoint"`,
		"firstKeptEntryId":     `"entry-b"`,
		"tokensBefore":         `4000`,
		"estimatedTokensAfter": `1200`,
	}
	for key, value := range want {
		if got := string(resultFields[key]); got != value {
			t.Fatalf("result.%s = %s, want %s", key, got, value)
		}
	}
	if got := string(resultFields["details"]); got != `{"readFiles":["a.go"],"modifiedFiles":[]}` {
		t.Fatalf("result.details = %s", got)
	}

	// A failed compaction reports the reason and no result at all.
	failed, err := json.Marshal(wire.AgentEvent{
		Type:         wire.AgentEventCompactionEnd,
		Reason:       wire.CompactionReasonThreshold,
		Aborted:      wire.Ptr(true),
		WillRetry:    wire.Ptr(false),
		ErrorMessage: "summarize failed",
	})
	if err != nil {
		t.Fatalf("marshal failure: %v", err)
	}
	if strings.Contains(string(failed), `"result"`) {
		t.Fatalf("compaction_end = %s, want no result on failure", failed)
	}
	if !strings.Contains(string(failed), `"errorMessage":"summarize failed"`) {
		t.Fatalf("compaction_end = %s", failed)
	}
}

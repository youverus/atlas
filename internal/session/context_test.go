package session

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/wire"
)

func entryFromJSON(t *testing.T, line string) Entry {
	t.Helper()
	entry, err := decodeEntry([]byte(line))
	if err != nil {
		t.Fatalf("decode entry %s: %v", line, err)
	}
	return entry
}

func entryID(t *testing.T, entries []Entry, id string) *string {
	t.Helper()
	for _, entry := range entries {
		if entry.ID == id {
			value := id
			return &value
		}
	}
	t.Fatalf("entry %s not in %d entries", id, len(entries))
	return nil
}

func TestBuildPathReturnsNothingWithoutALeaf(t *testing.T) {
	entries := []Entry{entryFromJSON(t, `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z"}`)}
	if path := BuildPath(entries, nil); len(path) != 0 {
		t.Fatalf("path = %+v, want empty", path)
	}
	if path := BuildPath(nil, nil); len(path) != 0 {
		t.Fatalf("path over no entries = %+v, want empty", path)
	}
}

func TestBuildPathFallsBackWhenTheLeafIsUnknown(t *testing.T) {
	first := entryFromJSON(t, `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z"}`)
	second := entryFromJSON(t, `{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-24T12:00:01.000Z"}`)
	path := BuildPath([]Entry{first, second}, wire.Ptr("missing"))
	if len(path) != 2 || path[0].ID != "a1" || path[1].ID != "a2" {
		t.Fatalf("path = %+v, want the whole chain", path)
	}
}

func TestBuildPathStopsAtACycle(t *testing.T) {
	first := entryFromJSON(t, `{"type":"message","id":"a1","parentId":"a2","timestamp":"2026-10-24T12:00:00.000Z"}`)
	second := entryFromJSON(t, `{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-24T12:00:01.000Z"}`)
	path := BuildPath([]Entry{first, second}, wire.Ptr("a1"))
	if len(path) != 2 {
		t.Fatalf("path = %+v, want 2 entries", path)
	}
}

func TestBuildContextEntriesKeepsOnlyTheNewestCompaction(t *testing.T) {
	entries := []Entry{
		entryFromJSON(t, `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"user","content":"one","timestamp":1}}`),
		entryFromJSON(t, `{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"system","content":"old prompt","timestamp":2}}`),
		entryFromJSON(t, `{"type":"message","id":"a3","parentId":"a2","timestamp":"2026-10-24T12:00:02.000Z","message":{"role":"assistant","content":"two","timestamp":3}}`),
		entryFromJSON(t, `{"type":"compaction","id":"a4","parentId":"a3","timestamp":"2026-10-24T12:00:03.000Z","summary":"digest","firstKeptEntryId":"a2","tokensBefore":42}`),
		entryFromJSON(t, `{"type":"message","id":"a5","parentId":"a4","timestamp":"2026-10-24T12:00:04.000Z","message":{"role":"user","content":"after","timestamp":5}}`),
	}
	contextEntries := BuildContextEntries(entries, entryID(t, entries, "a5"))
	ids := make([]string, len(contextEntries))
	for i, entry := range contextEntries {
		ids[i] = entry.ID
	}
	// The compaction stands in for a1, the kept range starts at a2, and the
	// system prompt inside it is dropped.
	if got := strings.Join(ids, ","); got != "a4,a3,a5" {
		t.Fatalf("context entries = %s, want a4,a3,a5", got)
	}

	context, err := BuildContext(entries, entryID(t, entries, "a5"))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	texts := messageTexts(context.Messages)
	if len(texts) != 3 {
		t.Fatalf("messages = %v, want 3", texts)
	}
	if !strings.Contains(texts[0], CompactionSummaryPrefix) || !strings.Contains(texts[0], "digest") {
		t.Fatalf("summary message = %q", texts[0])
	}
	if texts[1] != "two" || texts[2] != "after" {
		t.Fatalf("messages = %v", texts)
	}
}

func TestBuildContextEntriesWithSelfKeptCompaction(t *testing.T) {
	entries := []Entry{
		entryFromJSON(t, `{"type":"message","id":"b1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"user","content":"one","timestamp":1}}`),
		entryFromJSON(t, `{"type":"compaction","id":"b2","parentId":"b1","timestamp":"2026-10-24T12:00:01.000Z","summary":"everything","firstKeptEntryId":"b2","tokensBefore":9}`),
	}
	context, err := BuildContext(entries, entryID(t, entries, "b2"))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(context.Messages) != 1 || !strings.Contains(context.Messages[0].Text(), "everything") {
		t.Fatalf("messages = %v, want the summary only", messageTexts(context.Messages))
	}
}

func TestBuildProjectionTracksSettingsAndContextEdits(t *testing.T) {
	entries := []Entry{
		entryFromJSON(t, `{"type":"thinking_level_change","id":"c1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","thinkingLevel":"low"}`),
		entryFromJSON(t, `{"type":"model_change","id":"c2","parentId":"c1","timestamp":"2026-10-24T12:00:01.000Z","provider":"openai","modelId":"gpt"}`),
		entryFromJSON(t, `{"type":"message","id":"c3","parentId":"c2","timestamp":"2026-10-24T12:00:02.000Z","message":{"role":"user","content":"question","timestamp":3}}`),
		entryFromJSON(t, `{"type":"message","id":"c4","parentId":"c3","timestamp":"2026-10-24T12:00:03.000Z","message":{"role":"assistant","provider":"anthropic","model":"claude","content":[{"type":"text","text":"answer"}],"timestamp":4}}`),
		entryFromJSON(t, `{"type":"thinking_level_change","id":"c5","parentId":"c4","timestamp":"2026-10-24T12:00:04.000Z","thinkingLevel":"high"}`),
		entryFromJSON(t, `{"type":"context_edit","id":"c6","parentId":"c5","timestamp":"2026-10-24T12:00:05.000Z","targetId":"c3","replacement":{"content":[{"type":"text","text":"edited question"}]}}`),
	}
	projection, err := BuildProjection(entries, entryID(t, entries, "c6"))
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if projection.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", projection.ThinkingLevel)
	}
	if projection.Model == nil || projection.Model.Provider != "anthropic" || projection.Model.ModelID != "claude" {
		t.Fatalf("model = %+v, want the assistant message model", projection.Model)
	}
	texts := messageTexts(projection.Messages)
	if len(texts) != 2 || texts[0] != "edited question" || texts[1] != "answer" {
		t.Fatalf("messages = %v", texts)
	}
	// Every context entry is projected; state-only ones contribute no messages.
	if len(projection.Entries) != 6 {
		t.Fatalf("projected entries = %d, want 6", len(projection.Entries))
	}
	// The edited user message keeps the string form only for user roles.
	if projection.Messages[0].Content.IsPlain() {
		t.Fatalf("edited user content lost its block form: %+v", projection.Messages[0].Content)
	}
}

func TestBuildProjectionDropsAnEntryWithANullEdit(t *testing.T) {
	entries := []Entry{
		entryFromJSON(t, `{"type":"message","id":"d1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"user","content":"keep","timestamp":1}}`),
		entryFromJSON(t, `{"type":"message","id":"d2","parentId":"d1","timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"user","content":"drop","timestamp":2}}`),
		entryFromJSON(t, `{"type":"context_edit","id":"d3","parentId":"d2","timestamp":"2026-10-24T12:00:02.000Z","targetId":"d2","replacement":null}`),
	}
	context, err := BuildContext(entries, entryID(t, entries, "d3"))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if texts := messageTexts(context.Messages); len(texts) != 1 || texts[0] != "keep" {
		t.Fatalf("messages = %v, want [keep]", texts)
	}
}

func TestEntryMessagesConvertsSessionOnlyRoles(t *testing.T) {
	customEntry := entryFromJSON(t, `{"type":"custom_message","id":"e1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","customType":"note","content":"remember this","display":true}`)
	messages, err := EntryMessages(customEntry)
	if err != nil {
		t.Fatalf("custom message: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != wire.RoleUser || messages[0].Text() != "remember this" {
		t.Fatalf("custom messages = %+v", messages)
	}

	customRole := entryFromJSON(t, `{"type":"message","id":"e2","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"custom","content":"hook output","timestamp":7}}`)
	messages, err = EntryMessages(customRole)
	if err != nil {
		t.Fatalf("custom role: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != wire.RoleUser || messages[0].Text() != "hook output" {
		t.Fatalf("custom role messages = %+v", messages)
	}

	compactionRole := entryFromJSON(t, `{"type":"message","id":"e3","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"compactionSummary","summary":"older","timestamp":8}}`)
	messages, err = EntryMessages(compactionRole)
	if err != nil {
		t.Fatalf("compaction role: %v", err)
	}
	if len(messages) != 1 || messages[0].Text() != CompactionSummaryPrefix+"older"+CompactionSummarySuffix {
		t.Fatalf("compaction role messages = %+v", messages)
	}

	excluded := entryFromJSON(t, `{"type":"message","id":"e4","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"bashExecution","command":"ls","output":"a","exitCode":0,"excludeFromContext":true,"timestamp":9}}`)
	if messages, err = EntryMessages(excluded); err != nil || messages != nil {
		t.Fatalf("excluded bash message = %+v, %v", messages, err)
	}

	stateOnly := entryFromJSON(t, `{"type":"model_change","id":"e5","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","provider":"openai","modelId":"gpt"}`)
	if messages, err = EntryMessages(stateOnly); err != nil || messages != nil {
		t.Fatalf("state-only entry = %+v, %v", messages, err)
	}
}

func TestEntryMessagesRepairsNullContent(t *testing.T) {
	assistant := entryFromJSON(t, `{"type":"message","id":"f1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"assistant","content":null,"timestamp":1}}`)
	messages, err := EntryMessages(assistant)
	if err != nil {
		t.Fatalf("assistant with null content: %v", err)
	}
	if len(messages) != 1 || messages[0].Content.IsPlain() || messages[0].Content.Len() != 0 {
		t.Fatalf("assistant content = %+v", messages[0].Content)
	}

	system := entryFromJSON(t, `{"type":"message","id":"f2","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"system","content":null,"timestamp":1}}`)
	messages, err = EntryMessages(system)
	if err != nil {
		t.Fatalf("system with null content: %v", err)
	}
	if len(messages) != 1 || !messages[0].Content.IsPlain() || messages[0].Text() != "" {
		t.Fatalf("system content = %+v", messages[0].Content)
	}
}

func TestBashExecutionTextMatchesPi(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
		want  string
	}{
		{
			name:  "output",
			entry: entryFromJSON(t, `{"type":"message","id":"g1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"bashExecution","command":"ls","output":"a.go","exitCode":0,"timestamp":1}}`),
			want:  "Ran `ls`\n```\na.go\n```",
		},
		{
			name:  "no output",
			entry: entryFromJSON(t, `{"type":"message","id":"g2","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"bashExecution","command":"true","output":"","exitCode":0,"timestamp":1}}`),
			want:  "Ran `true`\n(no output)",
		},
		{
			name:  "failure",
			entry: entryFromJSON(t, `{"type":"message","id":"g3","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"bashExecution","command":"false","output":"","exitCode":1,"timestamp":1}}`),
			want:  "Ran `false`\n(no output)\n\nCommand exited with code 1",
		},
		{
			name:  "cancelled and truncated",
			entry: entryFromJSON(t, `{"type":"message","id":"g4","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"bashExecution","command":"sleep 99","output":"partial","exitCode":0,"cancelled":true,"truncated":true,"fullOutputPath":"/tmp/out.txt","timestamp":1}}`),
			want:  "Ran `sleep 99`\n```\npartial\n```\n\n(command cancelled)\n\n[Output truncated. Full output: /tmp/out.txt]",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			messages, err := EntryMessages(testCase.entry)
			if err != nil {
				t.Fatalf("entry messages: %v", err)
			}
			if len(messages) != 1 || messages[0].Text() != testCase.want {
				t.Fatalf("text = %q, want %q", messages[0].Text(), testCase.want)
			}
		})
	}
}

func TestCompactionEntryReplaysTheCapturedSystemMessage(t *testing.T) {
	entries := []Entry{
		entryFromJSON(t, `{"type":"message","id":"h1","parentId":null,"timestamp":"2026-10-24T12:00:00.000Z","message":{"role":"user","content":"one","timestamp":1}}`),
		entryFromJSON(t, `{"type":"compaction","id":"h2","parentId":"h1","timestamp":"2026-10-24T12:00:01.000Z","summary":"digest","firstKeptEntryId":"h2","tokensBefore":10,"systemMessage":{"role":"system","content":"captured prompt","timestamp":11}}`),
	}
	context, err := BuildContext(entries, entryID(t, entries, "h2"))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(context.Messages) != 2 {
		t.Fatalf("messages = %v, want the system prompt and the summary", messageTexts(context.Messages))
	}
	if context.Messages[0].Role != wire.RoleSystem || context.Messages[0].Text() != "captured prompt" {
		t.Fatalf("system message = %+v", context.Messages[0])
	}
	if !strings.Contains(context.Messages[1].Text(), "digest") {
		t.Fatalf("summary message = %q", context.Messages[1].Text())
	}
}

func TestEntryMessagesRejectsMalformedMessageJSON(t *testing.T) {
	entry := Entry{Type: TypeMessage, ID: "i1", Message: json.RawMessage(`"not a message"`)}
	if _, err := EntryMessages(entry); err == nil {
		t.Fatal("malformed message JSON decoded without an error")
	}
}

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/wire"
)

// epoch is a fixed clock origin so file names and entry timestamps are stable.
var epoch = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// testClock advances one second per call, which keeps entry order and file names
// distinct without sleeping.
func testClock() func() time.Time {
	current := epoch
	return func() time.Time {
		current = current.Add(time.Second)
		return current
	}
}

func newTestSession(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	manager, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return manager
}

func appendUser(t *testing.T, manager *Manager, text string) string {
	t.Helper()
	id, err := manager.AppendMessage(wire.UserMessage(text))
	if err != nil {
		t.Fatalf("append user message: %v", err)
	}
	return id
}

func appendAssistant(t *testing.T, manager *Manager, text string) string {
	t.Helper()
	id, err := manager.AppendMessage(wire.AssistantMessage(wire.TextContent(text)))
	if err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	return id
}

func messageTexts(messages []wire.Message) []string {
	texts := make([]string, len(messages))
	for i, message := range messages {
		texts[i] = message.Text()
	}
	return texts
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestCreateWritesNothingUntilConversationStarts(t *testing.T) {
	manager := newTestSession(t)

	// State-only entries stay in memory: opening pi and changing a setting must
	// not leave a session file behind.
	if _, err := manager.AppendThinkingLevelChange("high"); err != nil {
		t.Fatalf("append thinking level: %v", err)
	}
	if _, err := manager.AppendModelChange("anthropic", "claude"); err != nil {
		t.Fatalf("append model change: %v", err)
	}
	if _, err := manager.AppendLabelChange(manager.Entries()[0].ID, wire.Ptr("note")); err != nil {
		t.Fatalf("append label: %v", err)
	}
	if _, err := os.Stat(manager.File()); !os.IsNotExist(err) {
		t.Fatalf("session file exists before a conversation started: %v", err)
	}

	appendUser(t, manager, "hello")

	lines := readLines(t, manager.File())
	if len(lines) != 5 {
		t.Fatalf("file lines = %d, want header plus 4 entries: %v", len(lines), lines)
	}
	var header Header
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Version != CurrentVersion || header.ID != manager.ID() || header.Cwd != manager.Cwd() {
		t.Fatalf("unexpected header: %+v", header)
	}
	if want := header.ID + ".jsonl"; !strings.HasSuffix(filepath.Base(manager.File()), want) {
		t.Fatalf("file name %q does not end with %q", filepath.Base(manager.File()), want)
	}
}

func TestAppendThenReopenResumesContext(t *testing.T) {
	manager := newTestSession(t)
	if _, err := manager.AppendThinkingLevelChange("medium"); err != nil {
		t.Fatalf("append thinking level: %v", err)
	}
	appendUser(t, manager, "first")
	appendAssistant(t, manager, "second")

	path := manager.File()
	reopened, err := Open(path, "", "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if reopened.ID() != manager.ID() {
		t.Fatalf("id = %q, want %q", reopened.ID(), manager.ID())
	}
	if reopened.EntryCount() != manager.EntryCount() {
		t.Fatalf("entries = %d, want %d", reopened.EntryCount(), manager.EntryCount())
	}
	context, err := reopened.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	want := []string{"first", "second"}
	if got := messageTexts(context.Messages); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %v, want %v", got, want)
	}
	if context.ThinkingLevel != "medium" {
		t.Fatalf("thinking level = %q, want medium", context.ThinkingLevel)
	}

	// Appending after a resume keeps going from the last entry.
	appendUser(t, reopened, "third")
	lines := readLines(t, path)
	if len(lines) != 5 {
		t.Fatalf("lines after resume append = %d, want 5", len(lines))
	}
}

func TestAppendRawMessageKeepsUnknownRole(t *testing.T) {
	manager := newTestSession(t)
	// A bash execution is not a conversation by itself, so start one first.
	appendUser(t, manager, "hi")
	raw := json.RawMessage(`{"role":"bashExecution","command":"ls","output":"a.go","exitCode":0,"cancelled":false,"truncated":false,"timestamp":1}`)
	if _, err := manager.AppendRawMessage(raw); err != nil {
		t.Fatalf("append raw message: %v", err)
	}
	lines := readLines(t, manager.File())
	var entry struct {
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if string(entry.Message) != string(raw) {
		t.Fatalf("message = %s, want %s", entry.Message, raw)
	}
	context, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(context.Messages) != 2 || !strings.Contains(context.Messages[1].Text(), "Ran `ls`") {
		t.Fatalf("bash execution message = %+v", context.Messages)
	}
}

func TestBranchStartsNewChildWithoutRewritingHistory(t *testing.T) {
	manager := newTestSession(t)
	first := appendUser(t, manager, "one")
	appendAssistant(t, manager, "two")

	if err := manager.Branch(first); err != nil {
		t.Fatalf("branch: %v", err)
	}
	third := appendUser(t, manager, "three")

	path := manager.Path("")
	if len(path) != 2 || path[0].ID != first || path[1].ID != third {
		t.Fatalf("path = %+v, want first and third", path)
	}
	context, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if got := messageTexts(context.Messages); strings.Join(got, "|") != "one|three" {
		t.Fatalf("messages = %v, want [one three]", got)
	}

	// The abandoned sibling is still in the tree.
	children := manager.Children(first)
	if len(children) != 2 {
		t.Fatalf("children of first = %d, want 2", len(children))
	}
	if err := manager.Branch("missing"); err == nil {
		t.Fatal("branch to an unknown entry succeeded")
	}
}

func TestResetLeafMakesTheNextEntryARoot(t *testing.T) {
	manager := newTestSession(t)
	appendUser(t, manager, "one")
	manager.ResetLeaf()
	if leaf := manager.LeafID(); leaf != nil {
		t.Fatalf("leaf = %v, want nil", *leaf)
	}
	root := appendUser(t, manager, "two")

	entry, ok := manager.Entry(root)
	if !ok {
		t.Fatalf("entry %s missing", root)
	}
	if entry.ParentID != nil {
		t.Fatalf("parentId = %v, want nil", *entry.ParentID)
	}
	context, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if got := messageTexts(context.Messages); strings.Join(got, "|") != "two" {
		t.Fatalf("messages = %v, want [two]", got)
	}
	roots := manager.Tree()
	if len(roots) != 2 {
		t.Fatalf("tree roots = %d, want 2", len(roots))
	}
}

func TestBranchWithSummaryRecordsTheAbandonedLeaf(t *testing.T) {
	manager := newTestSession(t)
	first := appendUser(t, manager, "one")
	abandoned := appendAssistant(t, manager, "two")

	id, err := manager.BranchWithSummary(wire.Ptr(first), "they talked about two", nil, nil, nil)
	if err != nil {
		t.Fatalf("branch with summary: %v", err)
	}
	entry, ok := manager.Entry(id)
	if !ok {
		t.Fatalf("entry %s missing", id)
	}
	if entry.Type != TypeBranchSummary || entry.FromID != abandoned || entry.Summary != "they talked about two" {
		t.Fatalf("branch summary = %+v", entry)
	}
	if entry.ParentID == nil || *entry.ParentID != first {
		t.Fatalf("parent = %v, want %s", entry.ParentID, first)
	}
	context, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(context.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(context.Messages))
	}
	if text := context.Messages[1].Text(); !strings.Contains(text, BranchSummaryPrefix) || !strings.Contains(text, "they talked about two") {
		t.Fatalf("summary message = %q", text)
	}

	if _, err := manager.BranchWithSummary(wire.Ptr("missing"), "x", nil, nil, nil); err == nil {
		t.Fatal("branch with summary to an unknown entry succeeded")
	}
}

func TestLabelsTrackTheLatestChange(t *testing.T) {
	manager := newTestSession(t)
	target := appendUser(t, manager, "one")

	if _, err := manager.AppendLabelChange(target, wire.Ptr("checkpoint")); err != nil {
		t.Fatalf("label: %v", err)
	}
	label, ok := manager.Label(target)
	if !ok || label != "checkpoint" {
		t.Fatalf("label = %q, %v", label, ok)
	}
	roots := manager.Tree()
	if len(roots) != 1 || roots[0].Label == nil || *roots[0].Label != "checkpoint" {
		t.Fatalf("tree label = %+v", roots)
	}

	if _, err := manager.AppendLabelChange(target, nil); err != nil {
		t.Fatalf("clear label: %v", err)
	}
	if _, ok := manager.Label(target); ok {
		t.Fatal("label survived its removal")
	}
	if _, err := manager.AppendLabelChange("missing", wire.Ptr("x")); err == nil {
		t.Fatal("label on an unknown entry succeeded")
	}
}

func TestSessionNameUsesTheLatestNonEmptyValue(t *testing.T) {
	manager := newTestSession(t)
	if _, err := manager.AppendSessionInfo("  My\nSession  "); err != nil {
		t.Fatalf("append session info: %v", err)
	}
	if got := manager.Name(); got != "My Session" {
		t.Fatalf("name = %q, want %q", got, "My Session")
	}
	if _, err := manager.AppendSessionInfo("   "); err != nil {
		t.Fatalf("clear session info: %v", err)
	}
	if got := manager.Name(); got != "" {
		t.Fatalf("name = %q, want empty", got)
	}
}

func TestAppendCompactionCapturesTheSystemPrompt(t *testing.T) {
	manager := newTestSession(t)
	entryID := appendUser(t, manager, "hi")

	id, err := manager.AppendCompaction("digest", entryID, 1234, CompactionOptions{})
	if err != nil {
		t.Fatalf("append compaction: %v", err)
	}
	entry, ok := manager.Entry(id)
	if !ok {
		t.Fatalf("entry %s missing", id)
	}
	if entry.FirstKeptEntryID != entryID {
		t.Fatalf("firstKeptEntryId = %q, want %q", entry.FirstKeptEntryID, entryID)
	}
	if string(entry.TokensBefore) != "1234" {
		t.Fatalf("tokensBefore = %q, want 1234", entry.TokensBefore)
	}
	if len(entry.SystemMessage) != 0 {
		t.Fatalf("system message captured without a system prompt: %s", entry.SystemMessage)
	}
}

func TestAppendContextEditRejectsIneligibleTargets(t *testing.T) {
	manager := newTestSession(t)
	level, err := manager.AppendThinkingLevelChange("high")
	if err != nil {
		t.Fatalf("append thinking level: %v", err)
	}
	if _, err := manager.AppendContextEdit(level, json.RawMessage(`{"content":"x"}`)); err == nil {
		t.Fatal("context edit on a thinking level change succeeded")
	}
	if _, err := manager.AppendContextEdit("missing", json.RawMessage(`{"content":"x"}`)); err == nil {
		t.Fatal("context edit on an unknown entry succeeded")
	}

	first := appendUser(t, manager, "one")
	appendUser(t, manager, "two")
	if err := manager.Branch(first); err != nil {
		t.Fatalf("branch: %v", err)
	}
	offBranch := manager.Entries()[len(manager.Entries())-1]
	if _, err := manager.AppendContextEdit(offBranch.ID, json.RawMessage(`{"content":"x"}`)); err == nil {
		t.Fatal("context edit outside the active branch succeeded")
	}
}

func TestAppendContextEditNormalizesAssistantContent(t *testing.T) {
	manager := newTestSession(t)
	raw := json.RawMessage(`{"role":"assistant","content":"original","timestamp":1}`)
	id, err := manager.AppendRawMessage(raw)
	if err != nil {
		t.Fatalf("append raw message: %v", err)
	}
	if _, err := manager.AppendContextEdit(id, json.RawMessage(`{"content":"rewritten"}`)); err != nil {
		t.Fatalf("context edit: %v", err)
	}
	context, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(context.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(context.Messages))
	}
	message := context.Messages[0]
	if message.Content.IsPlain() {
		t.Fatalf("assistant content stayed a string: %+v", message.Content)
	}
	if got := message.Text(); got != "rewritten" {
		t.Fatalf("text = %q, want rewritten", got)
	}
	if text := string(raw); !strings.Contains(text, "original") {
		t.Fatalf("original entry lost its content: %s", text)
	}
}

func TestInMemorySessionNeverWritesFiles(t *testing.T) {
	dir := t.TempDir()
	manager, err := InMemory(dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("in-memory session: %v", err)
	}
	appendUser(t, manager, "hello")
	if manager.File() != "" {
		t.Fatalf("file = %q, want empty", manager.File())
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("in-memory session wrote to disk: %v %v", entries, err)
	}
	if _, err := manager.BuildContext(); err != nil {
		t.Fatalf("build context: %v", err)
	}
}

func TestContinueRecentPicksTheNewestSession(t *testing.T) {
	dir := t.TempDir()
	first, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	appendUser(t, first, "older")

	second, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	appendUser(t, second, "newer")

	// FindMostRecent only reads the file mtime, so make the order explicit.
	newest := time.Now().Add(time.Second)
	if err := os.Chtimes(second.File(), newest, newest); err != nil {
		t.Fatalf("touch %s: %v", second.File(), err)
	}

	resumed, err := ContinueRecent(dir, dir)
	if err != nil {
		t.Fatalf("continue recent: %v", err)
	}
	if resumed.ID() != second.ID() {
		t.Fatalf("resumed %q, want %q", resumed.ID(), second.ID())
	}
}

func TestExplicitSessionIDIsValidated(t *testing.T) {
	if !ValidSessionID("abc-123_X.y") {
		t.Fatal("a valid id was rejected")
	}
	for _, id := range []string{"", "-abc", "abc-", "a b", "a/b"} {
		if ValidSessionID(id) {
			t.Fatalf("id %q was accepted", id)
		}
	}

	dir := t.TempDir()
	manager, err := Create(dir, dir, Options{ID: "fixed-id", Clock: testClock()})
	if err != nil {
		t.Fatalf("create with an explicit id: %v", err)
	}
	if manager.ID() != "fixed-id" {
		t.Fatalf("id = %q, want fixed-id", manager.ID())
	}
	if manager.Dir() != dir || filepath.Dir(manager.File()) != dir {
		t.Fatalf("dir = %q, file = %q", manager.Dir(), manager.File())
	}
	if !manager.Persisted() {
		t.Fatal("a file-backed session reported itself as in-memory")
	}
	if _, err := Create(dir, dir, Options{ID: "bad id", Clock: testClock()}); err == nil {
		t.Fatal("create accepted an invalid id")
	}
}

func TestSetFileSwitchesSessions(t *testing.T) {
	first := newTestSession(t)
	appendUser(t, first, "one")
	second := newTestSession(t)
	appendUser(t, second, "two")

	if err := first.SetFile(second.File()); err != nil {
		t.Fatalf("set file: %v", err)
	}
	if first.ID() != second.ID() {
		t.Fatalf("id = %q, want %q", first.ID(), second.ID())
	}
	if first.EntryCount() != 1 {
		t.Fatalf("entries = %d, want 1", first.EntryCount())
	}
	leaf, ok := first.LeafEntry()
	if !ok || messageRoleOf(t, leaf) != "user" {
		t.Fatalf("leaf entry = %+v, %v", leaf, ok)
	}
}

func TestAppendRecordsStateOnlyEntries(t *testing.T) {
	manager := newTestSession(t)
	appendUser(t, manager, "hello")
	if _, err := manager.AppendMessage(wire.SystemMessage("You are pi.")); err != nil {
		t.Fatalf("append system message: %v", err)
	}
	usageID, err := manager.AppendUsage("compaction", "openai", "gpt", json.RawMessage(`{"input":10}`), "note")
	if err != nil {
		t.Fatalf("append usage: %v", err)
	}
	customID, err := manager.AppendCustomEntry("run-state", json.RawMessage(`{"step":2}`))
	if err != nil {
		t.Fatalf("append custom entry: %v", err)
	}
	messageID, err := manager.AppendCustomMessage("notice", json.RawMessage(`"heads up"`), false, nil)
	if err != nil {
		t.Fatalf("append custom message: %v", err)
	}

	usage, _ := manager.Entry(usageID)
	if usage.Type != TypeUsage || usage.Kind != "compaction" || usage.Provider != "openai" || usage.Model != "gpt" || usage.Note != "note" {
		t.Fatalf("usage entry = %+v", usage)
	}
	custom, _ := manager.Entry(customID)
	if custom.Type != TypeCustom || custom.CustomType != "run-state" {
		t.Fatalf("custom entry = %+v", custom)
	}
	message, _ := manager.Entry(messageID)
	if message.Type != TypeCustomMessage || message.Display == nil || *message.Display {
		t.Fatalf("custom message entry = %+v", message)
	}

	context, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	// The system message and the custom message reach the model; the usage and
	// custom entries stay out of it.
	if len(context.Messages) != 3 {
		t.Fatalf("messages = %v", messageTexts(context.Messages))
	}
	if context.Messages[1].Role != wire.RoleSystem || context.Messages[1].Text() != "You are pi." {
		t.Fatalf("system message = %+v", context.Messages[1])
	}
	if context.Messages[2].Role != wire.RoleUser || context.Messages[2].Text() != "heads up" {
		t.Fatalf("custom message = %+v", context.Messages[2])
	}

	// The compaction captures the replayed system prompt for a later resume.
	id, err := manager.AppendCompaction("digest", "", 42, CompactionOptions{})
	if err != nil {
		t.Fatalf("append compaction: %v", err)
	}
	compaction, _ := manager.Entry(id)
	if compaction.FirstKeptEntryID != id {
		t.Fatalf("firstKeptEntryId = %q, want its own id %q", compaction.FirstKeptEntryID, id)
	}
	if len(compaction.SystemMessage) == 0 {
		t.Fatal("compaction did not capture the system prompt")
	}
	var captured wire.Message
	if err := json.Unmarshal(compaction.SystemMessage, &captured); err != nil {
		t.Fatalf("decode captured system message: %v", err)
	}
	if captured.Role != wire.RoleSystem || captured.Text() != "You are pi." || captured.Timestamp == 0 {
		t.Fatalf("captured system message = %+v", captured)
	}

	// The manager-level context helpers agree with the package-level ones.
	if entries := manager.BuildContextEntries(); len(entries) == 0 {
		t.Fatal("manager context entries are empty")
	}
	projection, err := manager.BuildProjection()
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if projection.ThinkingLevel != DefaultThinkingLevel {
		t.Fatalf("thinking level = %q, want %q", projection.ThinkingLevel, DefaultThinkingLevel)
	}
}

// The first user or assistant message is the only thing that brings a session
// file into being. A conversation the user never talked to is not worth a file:
// leaving one behind on every quit or switch is what filled the session
// directory with empty sessions.
func TestAnEntrylessSessionLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	manager, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if manager.File() == "" {
		t.Fatal("a disk-backed session has no file path to create later")
	}
	// Naming the session is not a conversation.
	if _, err := manager.AppendSessionInfo("later"); err != nil {
		t.Fatalf("name session: %v", err)
	}
	if _, statErr := os.Stat(manager.File()); !os.IsNotExist(statErr) {
		t.Fatalf("an entry-less session has a file at %s", manager.File())
	}

	// The first message creates the file and takes the earlier name with it.
	appendUser(t, manager, "hello")
	loaded, err := LoadFile(manager.File())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Header.ID != manager.ID() {
		t.Fatalf("id = %q, want %q", loaded.Header.ID, manager.ID())
	}
	if len(loaded.Entries) != 2 || loaded.Entries[0].Name != "later" {
		t.Fatalf("entries = %+v, want the name and then the message", loaded.Entries)
	}

	// The file is live from here on: the next message appends.
	appendUser(t, manager, "again")
	loaded, err = LoadFile(manager.File())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded.Entries) != 3 {
		t.Fatalf("entries = %d, want the second message appended", len(loaded.Entries))
	}
}

// The default directory encodes cwd into its name, and that encoding is
// many-to-one: /x/a-b and /x/a/b land in one bucket. The header cwd is what
// says which project a session belongs to, so -continue must never hand back
// the other project's newest file.
func TestContinueRecentIgnoresAnotherProjectsSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	dashed := filepath.Join(root, "a-b")
	nested := filepath.Join(root, "a", "b")
	for _, cwd := range []string{dashed, nested} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cwd, err)
		}
	}
	if shared, other := DefaultDir(dashed, ""), DefaultDir(nested, ""); shared != other {
		t.Fatalf("the two projects do not share a bucket: %q vs %q", shared, other)
	}

	dashedManager, err := Create(dashed, "", Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create %s: %v", dashed, err)
	}
	appendUser(t, dashedManager, "from the dashed project")
	nestedManager, err := Create(nested, "", Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create %s: %v", nested, err)
	}
	appendUser(t, nestedManager, "from the nested project")
	// The other project's file is newer on disk, which is exactly the state
	// that made -continue cross projects: candidate order alone would pick it.
	newest := time.Now().Add(time.Second)
	if err := os.Chtimes(nestedManager.File(), newest, newest); err != nil {
		t.Fatalf("touch: %v", err)
	}

	resumed, err := ContinueRecent(dashed, "")
	if err != nil {
		t.Fatalf("continue in %s: %v", dashed, err)
	}
	if got := resumed.ID(); got != dashedManager.ID() {
		t.Fatalf("continued %q, want this project's session %q", got, dashedManager.ID())
	}

	other, err := ContinueRecent(nested, "")
	if err != nil {
		t.Fatalf("continue in %s: %v", nested, err)
	}
	if got := other.ID(); got != nestedManager.ID() {
		t.Fatalf("continued %q, want this project's session %q", got, nestedManager.ID())
	}
}

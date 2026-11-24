package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/wire"
)

func TestCreateBranchedSessionDropsLabelsAndRechains(t *testing.T) {
	manager := newTestSession(t)
	first := appendUser(t, manager, "one")
	if _, err := manager.AppendLabelChange(first, wire.Ptr("checkpoint")); err != nil {
		t.Fatalf("label: %v", err)
	}
	// The assistant message is a child of the label entry inside the chain.
	second := appendAssistant(t, manager, "two")
	oldFile := manager.File()
	oldID := manager.ID()

	leaf := appendUser(t, manager, "three")
	if err := manager.Branch(second); err != nil {
		t.Fatalf("branch: %v", err)
	}
	branched := appendUser(t, manager, "branch")
	if err := manager.Branch(leaf); err != nil {
		t.Fatalf("branch back: %v", err)
	}

	path, err := manager.CreateBranchedSession(branched)
	if err != nil {
		t.Fatalf("create branched session: %v", err)
	}
	if path == "" || path == oldFile {
		t.Fatalf("branched file = %q, want a new file", path)
	}
	if manager.ID() == oldID {
		t.Fatal("branched session reused the session id")
	}
	if manager.Header().ParentSession != oldFile {
		t.Fatalf("parentSession = %q, want %q", manager.Header().ParentSession, oldFile)
	}

	chain := manager.Path("")
	if len(chain) != 4 || chain[0].ID != first || chain[1].ID != second || chain[2].ID != branched {
		t.Fatalf("chain = %+v, want first, second, branch, and the label tail", chain)
	}
	if chain[3].Type != TypeLabel || chain[3].TargetID != first {
		t.Fatalf("tail entry = %+v, want the label on %q", chain[3], first)
	}
	if chain[2].ParentID == nil || *chain[2].ParentID != second {
		t.Fatalf("branch parent = %v, want %q", chain[2].ParentID, second)
	}

	// Route is still on the active branch: the label follows the chain as a tail.
	label, ok := manager.Label(first)
	if !ok || label != "checkpoint" {
		t.Fatalf("label = %q, %v", label, ok)
	}

	lines := readLines(t, path)
	if len(lines) != 5 {
		t.Fatalf("file lines = %d, want header, 3 entries, and the label: %v", len(lines), lines)
	}
	var header Header
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.ParentSession != oldFile || header.Cwd != manager.Cwd() {
		t.Fatalf("header = %+v", header)
	}
	var labelEntry Entry
	if err := json.Unmarshal([]byte(lines[4]), &labelEntry); err != nil {
		t.Fatalf("decode label entry: %v", err)
	}
	if labelEntry.Type != TypeLabel || labelEntry.TargetID != first || labelEntry.Label == nil || *labelEntry.Label != "checkpoint" {
		t.Fatalf("label entry = %+v", labelEntry)
	}
	if labelEntry.ParentID == nil || *labelEntry.ParentID != branched {
		t.Fatalf("label parent = %v, want %q", labelEntry.ParentID, branched)
	}

	// The new file loads back into the same shape.
	reopened, err := Open(path, "", "")
	if err != nil {
		t.Fatalf("reopen branched session: %v", err)
	}
	context, err := reopened.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if got := messageTexts(context.Messages); len(got) != 3 || got[2] != "branch" {
		t.Fatalf("messages = %v", got)
	}
}

func TestCreateBranchedSessionRepointsACompactionOffALabel(t *testing.T) {
	manager := newTestSession(t)
	first := appendUser(t, manager, "one")
	labelID, err := manager.AppendLabelChange(first, wire.Ptr("mark"))
	if err != nil {
		t.Fatalf("label: %v", err)
	}
	second := appendUser(t, manager, "two")
	compaction, err := manager.AppendCompaction("digest", labelID, 10, CompactionOptions{})
	if err != nil {
		t.Fatalf("compaction: %v", err)
	}

	if _, err := manager.CreateBranchedSession(compaction); err != nil {
		t.Fatalf("create branched session: %v", err)
	}
	entry, ok := manager.Entry(compaction)
	if !ok {
		t.Fatalf("compaction %s missing", compaction)
	}
	// The label is dropped, so the boundary follows the label's replacement,
	// which is the entry that came after it.
	if entry.FirstKeptEntryID != second {
		t.Fatalf("firstKeptEntryId = %q, want %q", entry.FirstKeptEntryID, second)
	}
	chain := manager.Path("")
	if len(chain) != 4 || chain[1].ID != second || chain[2].ID != compaction {
		t.Fatalf("chain = %+v, want first, second, compaction, and the label tail", chain)
	}
	if chain[3].Type != TypeLabel {
		t.Fatalf("tail entry = %+v, want the label", chain[3])
	}
}

func TestCreateBranchedSessionLeavesAnEmptyBranchUnflushed(t *testing.T) {
	manager := newTestSession(t)
	level, err := manager.AppendThinkingLevelChange("high")
	if err != nil {
		t.Fatalf("append thinking level: %v", err)
	}
	manager.ResetLeaf()

	path, err := manager.CreateBranchedSession(level)
	if err != nil {
		t.Fatalf("create branched session: %v", err)
	}
	// A branch without a conversation stays in memory, like a new session.
	if manager.EntryCount() != 1 {
		t.Fatalf("entries = %d, want 1", manager.EntryCount())
	}
	if _, err := manager.AppendThinkingLevelChange("low"); err != nil {
		t.Fatalf("append thinking level: %v", err)
	}
	if _, err := manager.CreateBranchedSession(manager.Entries()[0].ID); err != nil {
		t.Fatalf("second branched session: %v", err)
	}
	if path == "" {
		t.Fatal("branched file path is empty")
	}
}

func TestCreateBranchedSessionNeedsSomethingToBranch(t *testing.T) {
	manager := newTestSession(t)
	if _, err := manager.CreateBranchedSession("missing"); err == nil {
		t.Fatal("branching an empty session succeeded")
	}
	if _, err := manager.CreateBranchedSession(""); err == nil {
		t.Fatal("branching an empty session succeeded")
	}
}

func TestForkFromCopiesTheHistoryIntoAnotherProject(t *testing.T) {
	source := newTestSession(t)
	appendUser(t, source, "one")
	appendAssistant(t, source, "two")
	appendUser(t, source, "three")

	target := t.TempDir()
	forked, err := ForkFrom(source.File(), target, target, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if forked.Cwd() != target {
		t.Fatalf("cwd = %q, want %q", forked.Cwd(), target)
	}
	if forked.ID() == source.ID() {
		t.Fatal("fork reused the source session id")
	}
	if forked.Header().ParentSession != source.File() {
		t.Fatalf("parentSession = %q, want %q", forked.Header().ParentSession, source.File())
	}
	if filepath.Dir(forked.File()) != target {
		t.Fatalf("fork file = %q, want it under %q", forked.File(), target)
	}
	if forked.EntryCount() != source.EntryCount() {
		t.Fatalf("entries = %d, want %d", forked.EntryCount(), source.EntryCount())
	}
	context, err := forked.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if got := messageTexts(context.Messages); len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Fatalf("messages = %v", got)
	}

	// The fork keeps the source history byte for byte.
	data := readLines(t, forked.File())
	sourceLines := readLines(t, source.File())
	if len(data) != len(sourceLines) {
		t.Fatalf("fork lines = %d, source = %d", len(data), len(sourceLines))
	}
	for i := 1; i < len(sourceLines); i++ {
		if data[i] != sourceLines[i] {
			t.Fatalf("entry %d differs:\n got %s\nwant %s", i, data[i], sourceLines[i])
		}
	}

	// The fork is independent: appending does not touch the source.
	appendUser(t, forked, "forked")
	if source.EntryCount() != 3 {
		t.Fatalf("source entries = %d, want 3", source.EntryCount())
	}
}

func TestForkFromRejectsAnEmptySource(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if _, err := ForkFrom(empty, t.TempDir(), "", Options{}); err == nil {
		t.Fatal("forking a missing file succeeded")
	}
	if _, err := ForkFrom(writeFixture(t), t.TempDir(), "", Options{}); err == nil {
		t.Fatal("forking an empty file succeeded")
	}
}

func TestBranchedAndForkedSessionsCopyArtifactsIndependently(t *testing.T) {
	source := newTestSession(t)
	appendUser(t, source, "make chart")
	appendAssistant(t, source, "created chart")

	srcArt := filepath.Join(ArtifactDir(source.File()), "chart", "v1.dat")
	if err := os.MkdirAll(filepath.Dir(srcArt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcArt, []byte("<svg>v1</svg>"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	forked, err := ForkFrom(source.File(), target, target, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("ForkFrom: %v", err)
	}
	forkArt := filepath.Join(ArtifactDir(forked.File()), "chart", "v1.dat")
	got, err := os.ReadFile(forkArt)
	if err != nil || string(got) != "<svg>v1</svg>" {
		t.Fatalf("forked artifact = %q, err = %v", got, err)
	}

	if err := os.WriteFile(forkArt, []byte("<svg>fork-edit</svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcGot, err := os.ReadFile(srcArt)
	if err != nil || string(srcGot) != "<svg>v1</svg>" {
		t.Fatalf("source artifact mutated by fork: %q, err = %v", srcGot, err)
	}

	branchedPath, err := source.CreateBranchedSession("")
	if err != nil {
		t.Fatalf("CreateBranchedSession: %v", err)
	}
	branchArt := filepath.Join(ArtifactDir(branchedPath), "chart", "v1.dat")
	if branchGot, err := os.ReadFile(branchArt); err != nil || string(branchGot) != "<svg>v1</svg>" {
		t.Fatalf("branched artifact = %q, err = %v", branchGot, err)
	}
}

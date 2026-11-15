package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadFileSkipsBadLinesAndRepairsTheTail(t *testing.T) {
	path := writeFixture(t,
		`{"type":"session","version":3,"id":"sess","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x"}`,
		`{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"user","content":"one","timestamp":1}}`,
		`{"oops":`,
		``,
		`{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-24T12:00:02.000Z","message":{"role":"assistant","content":"two","timestamp":2}}`,
	)
	// The fixture has no trailing newline on the last entry.
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Empty() {
		t.Fatal("file reported as empty")
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.Entries[0].ID != "a1" || loaded.Entries[1].ID != "a2" {
		t.Fatalf("entry ids = %q, %q", loaded.Entries[0].ID, loaded.Entries[1].ID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("tail newline was not repaired: %q", string(data))
	}
	if loaded.Migrated {
		t.Fatal("current-version file reported as migrated")
	}
}

func TestLoadFileRejectsAFileThatIsNotASession(t *testing.T) {
	path := writeFixture(t, `{"type":"message","id":"a1"}`, `{"type":"message","id":"a2"}`)
	if _, err := LoadFile(path); !errors.Is(err, ErrNotSession) {
		t.Fatalf("error = %v, want ErrNotSession", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := Open(path, "", ""); err == nil {
		t.Fatal("open succeeded on a non-session file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("open modified a non-session file: %q -> %q", before, after)
	}
}

func TestLoadFileTreatsAnEmptyFileAsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Empty() {
		t.Fatal("empty file reported as a session")
	}

	manager, err := Open(path, "", "")
	if err != nil {
		t.Fatalf("open empty file: %v", err)
	}
	// Opening an empty file adopts it: the header is written immediately.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var header Header
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.ID != manager.ID() {
		t.Fatalf("header id = %q, want %q", header.ID, manager.ID())
	}
}

func TestOpenCreatesAMissingSessionFileOnDemand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.jsonl")
	manager, err := Open(path, "", "")
	if err != nil {
		t.Fatalf("open missing file: %v", err)
	}
	if manager.File() != path {
		t.Fatalf("file = %q, want %q", manager.File(), path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing file was created eagerly: %v", err)
	}
	appendUser(t, manager, "hello")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file was not created on demand: %v", err)
	}
}

func TestScanHeaderStopsAtTheFirstParsableLine(t *testing.T) {
	path := writeFixture(t,
		`not json at all`,
		`{"type":"session","version":3,"id":"scanned","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x","future":"kept"}`,
	)
	header, err := ScanHeader(path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if header == nil || header.ID != "scanned" {
		t.Fatalf("header = %+v", header)
	}
	if string(header.extra["future"]) != `"kept"` {
		t.Fatalf("unknown header field was dropped: %v", header.extra)
	}

	if header, err = ScanHeader(writeFixture(t, `{"type":"message","id":"a1"}`)); err != nil || header != nil {
		t.Fatalf("header = %+v, %v", header, err)
	}
}

func TestScanHeaderReportsAnOversizedPrefix(t *testing.T) {
	var builder strings.Builder
	for builder.Len() < headerScanLimit+1024 {
		builder.WriteString("\n")
	}
	builder.WriteString(`{"type":"session","version":3,"id":"late","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x"}`)
	path := filepath.Join(t.TempDir(), "late.jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ScanHeader(path); !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("error = %v, want ErrHeaderTooLarge", err)
	}

	// A full load still finds the header, which is why Open falls back to it.
	manager, err := Open(path, "", "")
	if err != nil {
		t.Fatalf("open large prefix: %v", err)
	}
	if manager.ID() != "late" {
		t.Fatalf("id = %q, want late", manager.ID())
	}
}

func TestCreateFileRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	file := &File{Header: Header{Version: CurrentVersion, ID: "one", Timestamp: "2026-10-24T12:00:00.000Z", Cwd: dir}}
	if err := CreateFile(path, file); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := CreateFile(path, file); err == nil {
		t.Fatal("second create overwrote the file")
	}
	if err := AppendEntry(path, Entry{Type: TypeCustom, ID: "a1", Timestamp: "2026-10-24T12:00:01.000Z"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].ID != "a1" {
		t.Fatalf("entries = %+v", loaded.Entries)
	}
}

func TestSaveFileReplaysUnmodifiedEntriesByteForByte(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"sess","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x"}`,
		// Unknown keys and an unusual key order must survive a load/save cycle.
		`{"parentId":null,"id":"a1","type":"custom","timestamp":"2026-10-24T12:00:01.000Z","customType":"note","data":{"nested":[1,2]},"future":"kept"}`,
		`{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-24T12:00:02.000Z","message":{"role":"user","content":"hi","timestamp":2,"unknown":true}}`,
	}
	path := writeFixture(t, lines...)
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(loaded.Entries[0].extra["future"]) != `"kept"` {
		t.Fatalf("unknown entry field was dropped: %v", loaded.Entries[0].extra)
	}
	if err := SaveFile(path, loaded); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	written := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(written) != len(lines) {
		t.Fatalf("lines = %d, want %d", len(written), len(lines))
	}
	for i := 1; i < len(lines); i++ {
		if written[i] != lines[i] {
			t.Fatalf("entry %d changed:\n got %s\nwant %s", i, written[i], lines[i])
		}
	}
	var header Header
	if err := json.Unmarshal([]byte(written[0]), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.ID != "sess" || header.Cwd != "/tmp/x" {
		t.Fatalf("header = %+v", header)
	}
}

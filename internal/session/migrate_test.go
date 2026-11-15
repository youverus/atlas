package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func messageRoleOf(t *testing.T, entry Entry) string {
	t.Helper()
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(entry.Message, &probe); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return probe.Role
}

func TestLoadFileMigratesV1ToCurrentVersion(t *testing.T) {
	// A v1 file: no version field, no entry ids, compaction indexed by line.
	path := writeFixture(t,
		`{"type":"session","id":"legacy","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x"}`,
		`{"type":"message","timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"user","content":"one","timestamp":1}}`,
		`{"type":"compaction","timestamp":"2026-10-24T12:00:02.000Z","summary":"digest","firstKeptEntryIndex":1,"tokensBefore":10}`,
		`{"type":"message","timestamp":"2026-10-24T12:00:03.000Z","message":{"role":"hookMessage","content":"hook output","timestamp":3}}`,
	)
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Header.Version != CurrentVersion {
		t.Fatalf("version = %d, want %d", loaded.Header.Version, CurrentVersion)
	}
	if !loaded.Migrated {
		t.Fatal("v1 file was not reported as migrated")
	}
	if len(loaded.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(loaded.Entries))
	}
	seen := map[string]bool{}
	for i, entry := range loaded.Entries {
		if entry.ID == "" {
			t.Fatalf("entry %d has no id: %+v", i, entry)
		}
		if seen[entry.ID] {
			t.Fatalf("duplicate id %q", entry.ID)
		}
		seen[entry.ID] = true
	}
	if loaded.Entries[0].ParentID != nil {
		t.Fatalf("first entry parent = %v, want nil", *loaded.Entries[0].ParentID)
	}
	if loaded.Entries[1].ParentID == nil || *loaded.Entries[1].ParentID != loaded.Entries[0].ID {
		t.Fatalf("second entry parent = %v, want %q", loaded.Entries[1].ParentID, loaded.Entries[0].ID)
	}
	// firstKeptEntryIndex 1 is the first non-header line, which is entries[0].
	if got := loaded.Entries[1].FirstKeptEntryID; got != loaded.Entries[0].ID {
		t.Fatalf("firstKeptEntryId = %q, want %q", got, loaded.Entries[0].ID)
	}
	if _, ok := loaded.Entries[1].extra["firstKeptEntryIndex"]; ok {
		t.Fatal("the legacy index field survived migration")
	}
	if role := messageRoleOf(t, loaded.Entries[2]); role != "custom" {
		t.Fatalf("hookMessage role = %q, want custom", role)
	}

	// Opening the file persists the migration.
	if _, err := Open(path, "", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"version":3`) {
		t.Fatalf("file was not rewritten at version 3:\n%s", text)
	}
	if strings.Contains(text, "hookMessage") || strings.Contains(text, "firstKeptEntryIndex") {
		t.Fatalf("legacy fields survived the rewrite:\n%s", text)
	}
}

func TestLoadFileMigratesV2HookMessages(t *testing.T) {
	path := writeFixture(t,
		`{"type":"session","version":2,"id":"v2","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x"}`,
		`{"type":"message","id":"keep1","parentId":null,"timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"hookMessage","content":"from a hook","timestamp":1}}`,
	)
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Migrated || loaded.Header.Version != CurrentVersion {
		t.Fatalf("version = %d, migrated = %v", loaded.Header.Version, loaded.Migrated)
	}
	// v2 already had ids, so they must be preserved rather than regenerated.
	if loaded.Entries[0].ID != "keep1" {
		t.Fatalf("id = %q, want keep1", loaded.Entries[0].ID)
	}
	if role := messageRoleOf(t, loaded.Entries[0]); role != "custom" {
		t.Fatalf("role = %q, want custom", role)
	}
	messages, err := EntryMessages(loaded.Entries[0])
	if err != nil {
		t.Fatalf("entry messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Text() != "from a hook" {
		t.Fatalf("migrated hook message = %+v", messages)
	}
}

func TestLoadFileLeavesCurrentVersionAlone(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"v3","timestamp":"2026-10-24T12:00:00.000Z","cwd":"/tmp/x"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"user","content":"one","timestamp":1}}`,
	}
	path := writeFixture(t, lines...)
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Migrated {
		t.Fatal("current-version file reported as migrated")
	}
	if lines[1] != string(loaded.Entries[0].Raw()) {
		t.Fatalf("raw entry = %s, want %s", loaded.Entries[0].Raw(), lines[1])
	}
}

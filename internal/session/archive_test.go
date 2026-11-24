package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Archiving means "keep the file, hide it from the lists that open
// conversations". Both halves matter: /recover still has to show it, so the
// mark cannot be deletion, and /resume must not, so the lists have to look.
func TestArchivingHidesASessionFromTheListsButKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	archived, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	appendUser(t, archived, "put me away")
	live, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create live: %v", err)
	}
	appendUser(t, live, "still here")

	if err := Archive(archived.File()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !Archived(archived.File()) {
		t.Fatal("the session does not report itself archived")
	}
	if Archived(live.File()) {
		t.Fatal("archiving one session marked another one")
	}
	if _, err := os.Stat(archived.File()); err != nil {
		t.Fatalf("the archived session file is gone: %v", err)
	}
	// The marker sits beside the session, so the v3 file stays byte-for-byte
	// what pi would have written.
	if marker := ArchiveMarker(archived.File()); marker != archived.File()+ArchiveSuffix {
		t.Fatalf("marker = %q", marker)
	}

	infos, err := List(dir, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != live.ID() {
		t.Fatalf("visible sessions = %+v, want only the live one", infos)
	}

	hidden, err := ListArchived(dir, "")
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(hidden) != 1 || hidden[0].ID != archived.ID() {
		t.Fatalf("archived sessions = %+v, want only the archived one", hidden)
	}

	// -continue must not walk back into a conversation the user put away.
	if got := FindMostRecent(dir, ""); got != live.File() {
		t.Fatalf("most recent = %q, want %q", got, live.File())
	}
	// A wide list is built on List, so it inherits the filter.
	if all, err := ListArchived(dir, live.Cwd()); err != nil || len(all) != 1 {
		t.Fatalf("archived for one project = %+v, %v", all, err)
	}
}

// Two calls say the same thing as one: the state is the marker, and it is
// already there.
func TestArchivingTwiceIsNotAnError(t *testing.T) {
	manager := newTestSession(t)
	appendUser(t, manager, "once")
	for i := 0; i < 2; i++ {
		if err := Archive(manager.File()); err != nil {
			t.Fatalf("archive %d: %v", i, err)
		}
	}
	if !Archived(manager.File()) {
		t.Fatal("the session is not archived")
	}
}

// Lazy creation means a conversation nobody talked to has no file, and hiding
// nothing is not archiving. The caller has to hear that rather than believe a
// transcript was put away.
func TestArchivingASessionWithoutAFileIsAnError(t *testing.T) {
	manager := newTestSession(t)
	if _, err := os.Stat(manager.File()); !os.IsNotExist(err) {
		t.Fatalf("the entry-less session already has a file: %v", err)
	}
	if err := Archive(manager.File()); !errors.Is(err, ErrNoSessionFile) {
		t.Fatalf("archive of an entry-less session = %v, want ErrNoSessionFile", err)
	}
	if err := Archive(""); !errors.Is(err, ErrNoSessionFile) {
		t.Fatalf("archive of no path = %v, want ErrNoSessionFile", err)
	}
	if err := Archive(filepath.Join(t.TempDir(), "missing.jsonl")); !errors.Is(err, ErrNoSessionFile) {
		t.Fatalf("archive of a missing file = %v, want ErrNoSessionFile", err)
	}
}

// Restoring is the inverse of archiving, and it has to be safe to repeat: the
// command layer runs it against whatever is on screen.
func TestUnarchiveBringsTheSessionBack(t *testing.T) {
	dir := t.TempDir()
	manager, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	appendUser(t, manager, "hello")

	// A session that was never archived is left alone, not reported as broken.
	if err := Unarchive(manager.File()); err != nil {
		t.Fatalf("unarchive a live session: %v", err)
	}
	if Archived(manager.File()) {
		t.Fatal("unarchiving marked the session")
	}
	if err := Unarchive(""); err != nil {
		t.Fatalf("unarchive of no path: %v", err)
	}

	if err := Archive(manager.File()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := Unarchive(manager.File()); err != nil {
			t.Fatalf("unarchive %d: %v", i, err)
		}
	}
	if Archived(manager.File()) {
		t.Fatal("the mark survived unarchiving")
	}
	infos, err := List(dir, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != manager.ID() {
		t.Fatalf("visible sessions = %+v, want the restored one", infos)
	}
}

// Deleting removes the session and its mark. The session goes first, so a
// delete that dies halfway leaves an inert marker rather than a live file the
// user believes he destroyed.
func TestDeleteFileRemovesTheSessionAndItsMark(t *testing.T) {
	dir := t.TempDir()
	manager, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	appendUser(t, manager, "goodbye")
	if err := Archive(manager.File()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	path := manager.File()
	if err := os.Mkdir(SubagentDir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(SubagentDir(path), "task.json"), []byte(`{"report":"evidence"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ArtifactDir(path), "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ArtifactDir(path), "demo", "v1.dat"), []byte("<h1>demo</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := DeleteFile(path); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, gone := range []string{path, ArchiveMarker(path), SubagentDir(path), ArtifactDir(path)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived the delete: %v", gone, err)
		}
	}
	if Archived(path) {
		t.Fatal("the marker still reports a deleted session as archived")
	}
	if infos, err := ListArchived(dir, ""); err != nil || len(infos) != 0 {
		t.Fatalf("archived sessions = %+v, %v", infos, err)
	}

	// Deleting what is already gone is not an error: the caller asked for the
	// file to not be there, and it is not.
	for i := 0; i < 2; i++ {
		if err := DeleteFile(path); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if err := DeleteFile(""); err != nil {
		t.Fatalf("delete of no path: %v", err)
	}
}

// Archiving must not move the file: the modification time is what the picker
// sorts by, and a session the user restores has to come back where it was in
// the list.
func TestArchivingLeavesTheFileAlone(t *testing.T) {
	manager := newTestSession(t)
	appendUser(t, manager, "keep my bytes")
	before, err := os.ReadFile(manager.File())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(manager.File(), stamp, stamp); err != nil {
		t.Fatalf("touch: %v", err)
	}

	if err := Archive(manager.File()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	after, err := os.ReadFile(manager.File())
	if err != nil {
		t.Fatalf("read after archive: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("archiving rewrote the session file:\nbefore %q\nafter  %q", before, after)
	}
	info, err := os.Stat(manager.File())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("mod time = %v, want %v", info.ModTime(), stamp)
	}
}

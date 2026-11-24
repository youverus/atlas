package project

import (
	"os"
	"path/filepath"
	"testing"
)

func store(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), FileName))
}

// A window that has never had a project added must open with an empty list, not
// with an error: the file is written by the first Add and by nothing else.
func TestMissingFileIsAnEmptyList(t *testing.T) {
	if list := store(t).List(); len(list) != 0 {
		t.Fatalf("list = %v, want empty", list)
	}
}

func TestAddAppendsInOrder(t *testing.T) {
	s := store(t)
	first := t.TempDir()
	second := t.TempDir()
	for _, path := range []string{first, second} {
		if _, err := s.Add(path); err != nil {
			t.Fatalf("add %s: %v", path, err)
		}
	}
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("list = %v, want two entries", list)
	}
	if list[0].Path != first || list[1].Path != second {
		t.Fatalf("order = %v, want the order they were added", list)
	}
	if list[0].Added == "" {
		t.Fatal("added time is empty")
	}
}

// The same folder added twice is one row. The second spelling may be a relative
// path, a trailing separator, or the same directory with the symlinks resolved:
// a picker hands back the resolved form, the reader may type either.
func TestAddDeduplicatesTheSameDirectory(t *testing.T) {
	s := store(t)
	dir := t.TempDir()
	if _, err := s.Add(dir); err != nil {
		t.Fatalf("add: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	spellings := []string{dir + string(filepath.Separator), resolved, filepath.Join(dir, ".")}
	for _, spelling := range spellings {
		if _, err := s.Add(spelling); err != nil {
			t.Fatalf("add %q: %v", spelling, err)
		}
	}
	if list := s.List(); len(list) != 1 {
		t.Fatalf("list = %v, want one entry", list)
	}
	if !s.Has(resolved) {
		t.Fatal("Has says the resolved path is not listed")
	}
}

func TestAddRejectsMissingPathAndFile(t *testing.T) {
	s := store(t)
	if _, err := s.Add(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("adding a missing directory succeeded")
	}
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := s.Add(file); err == nil {
		t.Fatal("adding a file succeeded")
	}
	if _, err := s.Add(""); err == nil {
		t.Fatal("adding an empty path succeeded")
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("list = %v, want empty after three rejections", list)
	}
}

// Removing a project is forgetting a shortcut, not deleting a directory.
func TestRemoveLeavesTheDirectory(t *testing.T) {
	s := store(t)
	dir := t.TempDir()
	if _, err := s.Add(dir); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Remove(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("list = %v, want empty", list)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the directory went with the project: %v", err)
	}
	if err := s.Remove(dir); err == nil {
		t.Fatal("removing a folder that is not listed succeeded")
	}
}

// The list is hand-editable, so a broken file has to cost the list, not the
// window.
func TestCorruptFileIsAnEmptyList(t *testing.T) {
	s := store(t)
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("list = %v, want empty", list)
	}
	// And writing after the break replaces the file rather than appending to it.
	dir := t.TempDir()
	if _, err := s.Add(dir); err != nil {
		t.Fatalf("add: %v", err)
	}
	if list := s.List(); len(list) != 1 || list[0].Path != dir {
		t.Fatalf("list = %v, want only the folder just added", list)
	}
}

// An entry without a path is a row nobody can open, so it is dropped on read
// instead of being drawn as a blank project.
func TestEmptyPathsAreDroppedOnRead(t *testing.T) {
	s := store(t)
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"projects":[{"path":""},{"path":"   "},{"path":"/tmp"}]}`
	if err := os.WriteFile(s.Path(), []byte(data), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Path != "/tmp" {
		t.Fatalf("list = %v, want only /tmp", list)
	}
}

func TestAddNamedStoresAndUpdatesDisplayName(t *testing.T) {
	s := store(t)
	dir := t.TempDir()

	added, err := s.AddNamed(dir, "  Core Service  ")
	if err != nil {
		t.Fatalf("add named: %v", err)
	}
	if added.Name != "Core Service" {
		t.Fatalf("added.Name = %q, want %q", added.Name, "Core Service")
	}

	// Re-adding without a name keeps the stored custom name.
	again, err := s.Add(dir)
	if err != nil {
		t.Fatalf("re-add without name: %v", err)
	}
	if again.Name != "Core Service" {
		t.Fatalf("again.Name = %q, want preserved %q", again.Name, "Core Service")
	}

	// Re-adding with a new non-empty name updates the stored label.
	renamed, err := s.AddNamed(dir, "Renamed Service")
	if err != nil {
		t.Fatalf("re-add with new name: %v", err)
	}
	if renamed.Name != "Renamed Service" {
		t.Fatalf("renamed.Name = %q, want %q", renamed.Name, "Renamed Service")
	}
	list := s.List()
	if len(list) != 1 || list[0].Name != "Renamed Service" {
		t.Fatalf("persisted list = %+v, want one entry with updated name", list)
	}
}

func TestRemoteProjectsAreIsolatedFromLocalPaths(t *testing.T) {
	s := store(t)
	localDir := t.TempDir()

	if _, err := s.AddNamed(localDir, "Local Copy"); err != nil {
		t.Fatalf("add local: %v", err)
	}
	if _, err := s.AddRemote("dev-1", localDir+"/", "Remote Copy"); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	if _, err := s.AddRemote("dev-2", "/home/ubuntu/workspace/", "GPU Work"); err != nil {
		t.Fatalf("add non-existent remote path: %v", err)
	}
	if _, err := s.AddRemote("local", "/tmp", "Bad"); err == nil {
		t.Fatal("AddRemote accepted local device id")
	}
	if _, err := s.AddRemote("dev-1", "relative/path", "Bad"); err == nil {
		t.Fatal("AddRemote accepted relative path")
	}

	// Removing local copy must leave remote projects untouched.
	if err := s.Remove(localDir); err != nil {
		t.Fatalf("remove local: %v", err)
	}
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("list after local remove = %+v, want 2 remote projects", list)
	}
	if list[0].DeviceID != "dev-1" || list[0].Path != localDir || list[0].Name != "Remote Copy" {
		t.Fatalf("remote[0] = %+v", list[0])
	}
	if list[1].DeviceID != "dev-2" || list[1].Path != "/home/ubuntu/workspace" || list[1].Name != "GPU Work" {
		t.Fatalf("remote[1] = %+v", list[1])
	}

	if err := s.RemoveForDevice("dev-1", localDir); err != nil {
		t.Fatalf("remove remote dev-1: %v", err)
	}
	if len(s.List()) != 1 {
		t.Fatalf("list after remote remove = %+v, want 1", s.List())
	}
}

package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusGroups(t *testing.T) {
	for _, status := range []Status{StatusQueued, StatusRunning, StatusStopping} {
		if !status.Active() || status.Terminal() {
			t.Errorf("%s: want active and not terminal", status)
		}
	}
	for _, status := range []Status{StatusSucceeded, StatusFailed, StatusCanceled, StatusLost} {
		if status.Active() || !status.Terminal() {
			t.Errorf("%s: want terminal and not active", status)
		}
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		want     bool
	}{
		{StatusQueued, StatusRunning, true},
		{StatusQueued, StatusCanceled, true},
		{StatusQueued, StatusFailed, true},
		{StatusRunning, StatusStopping, true},
		{StatusRunning, StatusSucceeded, true},
		{StatusRunning, StatusQueued, false},
		{StatusStopping, StatusCanceled, true},
		{StatusStopping, StatusSucceeded, true},
		{StatusSucceeded, StatusRunning, false},
		{StatusCanceled, StatusSucceeded, false},
		{StatusLost, StatusRunning, false},
		// Staying put is legal, which is what makes a repeated report harmless.
		{StatusRunning, StatusRunning, true},
		{StatusSucceeded, StatusSucceeded, true},
	}
	for _, test := range cases {
		if got := canTransition(test.from, test.to); got != test.want {
			t.Errorf("%s -> %s: got %v, want %v", test.from, test.to, got, test.want)
		}
	}
}

func TestAgeUsesStartAndEnd(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	record := Task{CreatedAtMs: 900_000, StartedAtMs: 950_000}
	if got := record.Age(now); got != 50*time.Second {
		t.Errorf("running age: got %v", got)
	}
	// A finished task reports its duration, not the distance to now: the age of
	// something that ended does not keep growing.
	record.EndedAtMs = 980_000
	if got := record.Age(now); got != 30*time.Second {
		t.Errorf("finished age: got %v", got)
	}
	// A clock that went backwards must not produce a negative age.
	running := Task{CreatedAtMs: 900_000}
	if got := running.Age(time.UnixMilli(1)); got != 0 {
		t.Errorf("backwards clock: got %v", got)
	}
}

func TestNewIDIsPrefixedAndUnique(t *testing.T) {
	taken := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newID(taken)
		if len(id) != len("bg_")+12 || id[:3] != "bg_" {
			t.Fatalf("unexpected id shape: %q", id)
		}
		if taken[id] {
			t.Fatalf("duplicate id: %q", id)
		}
		taken[id] = true
	}
}

func TestStoreAppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(func() string { return dir })

	if _, err := store.Load(); err != nil {
		t.Fatalf("load of a missing file: %v", err)
	}
	first := Task{ID: "bg_1", Status: StatusQueued, CreatedAtMs: 20}
	if err := store.Append(first); err != nil {
		t.Fatalf("append: %v", err)
	}
	// The second record for the same id wins, which is what makes the file
	// append-only without making it a log of every state it ever had.
	running := first
	running.Status = StatusRunning
	running.PGID = 4242
	if err := store.Append(running); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Append(Task{ID: "bg_2", Status: StatusQueued, CreatedAtMs: 10}); err != nil {
		t.Fatalf("append: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(loaded))
	}
	// Oldest first, ordered by creation rather than by the order they were
	// appended.
	if loaded[0].ID != "bg_2" || loaded[1].ID != "bg_1" {
		t.Fatalf("unexpected order: %v %v", loaded[0].ID, loaded[1].ID)
	}
	if loaded[1].Status != StatusRunning || loaded[1].PGID != 4242 {
		t.Fatalf("last record did not win: %+v", loaded[1])
	}
}

func TestStoreSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	// A cut-off write is the only way to get a bad line, and it must cost the
	// one record rather than the whole file.
	content := `{"id":"bg_1","status":"running"}` + "\n" + `{"id":"bg_2"` + "\n\n" +
		`{"id":"","status":"running"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := NewStore(func() string { return dir }).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "bg_1" {
		t.Fatalf("unexpected records: %+v", loaded)
	}
}

func TestStoreCreatesDirectoryAndUsesTightMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	store := NewStore(func() string { return dir })
	if err := store.Append(Task{ID: "bg_1", CreatedAtMs: 1}); err != nil {
		t.Fatalf("append: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("record file mode: got %o", mode)
	}
}

func TestStorePathFollowsDirectory(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	dir := first
	store := NewStore(func() string { return dir })
	if got, want := store.Path(), filepath.Join(first, FileName); got != want {
		t.Fatalf("path: got %s, want %s", got, want)
	}
	// The directory is asked on every call so a resumed session writes to its
	// own project.
	dir = second
	if got, want := store.Path(), filepath.Join(second, FileName); got != want {
		t.Fatalf("path after a move: got %s, want %s", got, want)
	}
}

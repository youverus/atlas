package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A nil Spill keeps the behaviour every caller had before 0.12.8: the OS temp
// directory. That is what makes the argument optional in four places.
func TestSpillWithoutADirectoryUsesTheTempDir(t *testing.T) {
	var none *Spill
	if got := none.Dir(); got != os.TempDir() {
		t.Fatalf("nil spill dir = %q, want %q", got, os.TempDir())
	}
	if got := NewSpill(nil).Dir(); got != os.TempDir() {
		t.Fatalf("empty func dir = %q, want %q", got, os.TempDir())
	}
	if got := NewSpill(func() string { return "" }).Dir(); got != os.TempDir() {
		t.Fatalf("func returning empty dir = %q, want %q", got, os.TempDir())
	}
	// Write and Create must work through a nil receiver too, or every caller
	// would need to check before spilling.
	path, err := none.Write("pi-test", ".log", []byte("hello\n"))
	if err != nil {
		t.Fatalf("nil Write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello\n" {
		t.Fatalf("read back = %q, %v", data, err)
	}
}

// The directory is asked for on every write, not remembered: a /resume moves
// the session without rebuilding the tools that spill into it.
func TestSpillAsksForItsDirectoryEveryTime(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	current := first
	spill := NewSpill(func() string { return current })

	one, err := spill.Write("pi-test", ".log", []byte("first"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	current = second
	two, err := spill.Write("pi-test", ".log", []byte("second"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if filepath.Dir(one) != first {
		t.Fatalf("first spill in %q, want %q", filepath.Dir(one), first)
	}
	if filepath.Dir(two) != second {
		t.Fatalf("second spill in %q, want %q — the directory was remembered", filepath.Dir(two), second)
	}
}

// A missing directory is created rather than reported: the session directory of
// a conversation that never wrote a message does not exist yet, and a spill is
// not the thing that should fail over it.
func TestSpillCreatesAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "--proj--", "spill")
	path, err := NewSpill(func() string { return dir }).Write("pi-test", ".log", []byte("x"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	if filepath.Dir(path) != dir {
		t.Fatalf("spill in %q, want %q", filepath.Dir(path), dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %o, want 700", perm)
	}
}

// Output can carry private data, so the file is 0600 — and two spills never
// share a name, which is what keeps a second command from overwriting the first
// one's output.
func TestSpillWritesAPrivateUniquelyNamedFile(t *testing.T) {
	dir := t.TempDir()
	spill := NewSpill(func() string { return dir })

	one, err := spill.Write("pi-bash", ".log", []byte("one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	two, err := spill.Write("pi-bash", ".log", []byte("two"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(one); _ = os.Remove(two) })

	if one == two {
		t.Fatalf("both spills took the same path %q", one)
	}
	if base := filepath.Base(one); !strings.HasPrefix(base, "pi-bash-") || !strings.HasSuffix(base, ".log") {
		t.Fatalf("name = %q, want pi-bash-*.log", base)
	}
	info, err := os.Stat(one)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
}

// The clause is one string so four tools say "where it went" the same way. An
// empty path produces nothing, which is how a failed spill keeps the truncated
// text readable instead of ending the sentence with a colon and no filename.
func TestSpillNoticeNamesThePath(t *testing.T) {
	if got := SpillNotice(""); got != "" {
		t.Fatalf("empty path = %q, want nothing", got)
	}
	got := SpillNotice("/tmp/x/../spill/pi-bash-1.log")
	if got != "the full output is in /tmp/spill/pi-bash-1.log" {
		t.Fatalf("notice = %q", got)
	}
}

package webhost

import (
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/project"
)

// TestConfineNeverLeavesTheRoot guards the only security decision this package
// makes: a path from the window must not reach outside the working directory.
//
// The guarantee is containment, not rejection. A path is anchored with a
// leading slash before cleaning, so "../.." climbs to the root and stops there;
// "/../etc/passwd" and "a/../../etc/passwd" both normalise to "etc/passwd"
// inside the root. That is deliberately safer than returning an error: the
// frontend never has to reason about which of its paths were refused.
func TestConfineNeverLeavesTheRoot(t *testing.T) {
	root := "/work/project"
	cases := []struct {
		path string
		want string
	}{
		{"", ""},
		{"main.go", "main.go"},
		{"internal/httpapi", "internal/httpapi"},
		{"/main.go", "main.go"},
		{"a/../b", "b"},
		{"..", ""},
		{"../etc/passwd", "etc/passwd"},
		{"a/../../etc/passwd", "etc/passwd"},
		{"/../../etc/passwd", "etc/passwd"},
		{"a/../../..", ""},
	}
	for _, test := range cases {
		got, err := confine(root, test.path)
		if err != nil {
			t.Fatalf("confine(%q) = %v, want %q", test.path, err, test.want)
		}
		if got != test.want {
			t.Fatalf("confine(%q) = %q, want %q", test.path, got, test.want)
		}
		if got == ".." || strings.HasPrefix(got, "../") {
			t.Fatalf("confine(%q) escaped to %q", test.path, got)
		}
	}
}

func TestIsBinaryDetectsNulBytes(t *testing.T) {
	if isBinary([]byte("hello world")) {
		t.Fatal("text was reported as binary")
	}
	if !isBinary([]byte("ab\x00cd")) {
		t.Fatal("a NUL byte was not reported as binary")
	}
	if isBinary(nil) {
		t.Fatal("an empty file was reported as binary")
	}
}

// The rail's order is the order the reader built it in, with the folder
// conversations start in always first. Listing that folder twice would draw the
// same conversations under two headers.
func TestMergeGroupsPutsTheDefaultFirstAndDropsDuplicates(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	stored := []project.Project{{Path: first}, {Path: second}}
	drawn := mergeGroups(first, stored)
	if len(drawn) != 2 {
		t.Fatalf("drawn = %v, want the default and the other project", drawn)
	}
	if drawn[0] != first || drawn[1] != second {
		t.Fatalf("drawn = %v, want the default first", drawn)
	}

	// The same folder with the symlinks resolved is the same project: a picker
	// hands back the resolved form while the default is the path as recorded.
	resolved, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if drawn := mergeGroups(first, []project.Project{{Path: resolved}}); len(drawn) != 1 {
		t.Fatalf("drawn = %v, want one group for one directory", drawn)
	}
}

func TestSameDirResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if !sameDir(dir, resolved) {
		t.Fatalf("sameDir(%q, %q) = false, want true", dir, resolved)
	}
	if sameDir(dir, "") || sameDir("", dir) {
		t.Fatal("an empty path matched a directory")
	}
	if sameDir(dir, t.TempDir()) {
		t.Fatal("two different directories matched")
	}
}

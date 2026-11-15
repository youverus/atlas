package fs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLs(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		"zeta.txt":      "z",
		"Alpha.txt":     "a",
		"beta.txt":      "b",
		".hidden":       "h",
		"folder/":       "",
		"folder/x.txt":  "x",
		"another/deep/": "",
	})
	result := run(t, pick(t, cwd, "ls"), `{}`)

	want := ".hidden\nAlpha.txt\nanother/\nbeta.txt\nfolder/\nzeta.txt"
	if got := text(t, result); got != want {
		t.Fatalf("ls returned:\n%q\nwant:\n%q", got, want)
	}
	if result.Details != nil {
		t.Fatalf("an untruncated ls should not set details: %s", result.Details)
	}
}

func TestLsEmptyDirectory(t *testing.T) {
	cwd := newCwd(t, map[string]string{"empty/": ""})
	result := run(t, pick(t, cwd, "ls"), `{"path":"empty"}`)

	if got := text(t, result); got != "(empty directory)" {
		t.Fatalf("ls returned %q", got)
	}
}

func TestLsLimit(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 5; i++ {
		files[fmt.Sprintf("f%d.txt", i)] = "x"
	}
	cwd := newCwd(t, files)

	result := run(t, pick(t, cwd, "ls"), `{"limit":2}`)
	got := text(t, result)
	if !strings.HasPrefix(got, "f0.txt\nf1.txt") {
		t.Fatalf("ls with a limit returned %q", got)
	}
	if !strings.Contains(got, "[2 entries limit reached. Use limit=4 for more]") {
		t.Fatalf("missing entry limit notice: %q", got)
	}

	var details lsDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.EntryLimitReached != 2 {
		t.Fatalf("entryLimitReached = %d, want 2", details.EntryLimitReached)
	}
}

func TestLsErrors(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "x"})

	err := runError(t, pick(t, cwd, "ls"), `{"path":"missing"}`)
	if !strings.Contains(err.Error(), "Path not found: ") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %q", err)
	}

	err = runError(t, pick(t, cwd, "ls"), `{"path":"a.txt"}`)
	if !strings.Contains(err.Error(), "Not a directory: ") {
		t.Fatalf("error = %q", err)
	}
}

func TestLsDefaultsToCwd(t *testing.T) {
	cwd := newCwd(t, map[string]string{"only.txt": "x"})
	result := run(t, pick(t, cwd, "ls"), `{"path":"."}`)

	if got := text(t, result); got != "only.txt" {
		t.Fatalf("ls returned %q", got)
	}
}

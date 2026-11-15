package fs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pi-go/internal/tool"
)

func TestWriteCreatesParentDirectories(t *testing.T) {
	cwd := newCwd(t, map[string]string{})
	result := run(t, pick(t, cwd, "write"), `{"path":"deep/nested/file.txt","content":"hello"}`)

	if got := text(t, result); got != "Successfully wrote to deep/nested/file.txt" {
		t.Fatalf("write returned %q", got)
	}
	if got := readFile(t, cwd, "deep/nested/file.txt"); got != "hello" {
		t.Fatalf("file content = %q", got)
	}
}

func TestWriteOverwrites(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "old content that is long"})
	run(t, pick(t, cwd, "write"), `{"path":"a.txt","content":"new"}`)

	if got := readFile(t, cwd, "a.txt"); got != "new" {
		t.Fatalf("file content = %q, want the full overwrite", got)
	}
}

func TestWriteRequiresPath(t *testing.T) {
	cwd := newCwd(t, map[string]string{})
	runError(t, pick(t, cwd, "write"), `{"content":"x"}`)
}

func TestEditSingle(t *testing.T) {
	cwd := newCwd(t, map[string]string{"main.go": "package main\n\nfunc main() {\n\tprintln(1)\n}\n"})
	result := run(t, pick(t, cwd, "edit"),
		`{"path":"main.go","edits":[{"oldText":"println(1)","newText":"println(2)"}]}`)

	if got := text(t, result); got != "Successfully replaced 1 block(s) in main.go." {
		t.Fatalf("edit returned %q", got)
	}
	want := "package main\n\nfunc main() {\n\tprintln(2)\n}\n"
	if got := readFile(t, cwd, "main.go"); got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

func TestEditMultipleDisjointBlocks(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		"main.go": "package main\n\ntype Old struct{}\n\nfunc name() string { return \"old\" }\n",
	})
	result := run(t, pick(t, cwd, "edit"), `{"path":"main.go","edits":[
		{"oldText":"type Old struct{}","newText":"type Fresh struct{}"},
		{"oldText":"return \"old\"","newText":"return \"fresh\""}
	]}`)

	if got := text(t, result); got != "Successfully replaced 2 block(s) in main.go." {
		t.Fatalf("edit returned %q", got)
	}
	content := readFile(t, cwd, "main.go")
	if !strings.Contains(content, "type Fresh struct{}") || !strings.Contains(content, `return "fresh"`) {
		t.Fatalf("both edits should be present: %q", content)
	}
}

// Edits are matched against the original file, so an edit cannot depend on an
// earlier one having been applied.
func TestEditMatchesAgainstOriginalFile(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "alpha\n"})
	runError(t, pick(t, cwd, "edit"), `{"path":"a.txt","edits":[
		{"oldText":"alpha","newText":"beta"},
		{"oldText":"beta","newText":"gamma"}
	]}`)
}

func TestEditAcceptsEditsAsJSONString(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "alpha\n"})
	run(t, pick(t, cwd, "edit"),
		`{"path":"a.txt","edits":"[{\"oldText\":\"alpha\",\"newText\":\"beta\"}]"}`)

	if got := readFile(t, cwd, "a.txt"); got != "beta\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditAcceptsSingleEditObject(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "alpha\n"})
	run(t, pick(t, cwd, "edit"), `{"path":"a.txt","edits":{"oldText":"alpha","newText":"beta"}}`)

	if got := readFile(t, cwd, "a.txt"); got != "beta\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditAcceptsLegacyTopLevelFields(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "alpha\n"})
	run(t, pick(t, cwd, "edit"), `{"path":"a.txt","oldText":"alpha","newText":"beta"}`)

	if got := readFile(t, cwd, "a.txt"); got != "beta\n" {
		t.Fatalf("file content = %q", got)
	}
}

// A model that copies text with straightened quotes still lands the edit: the
// fallback compares a normalized view.
func TestEditFuzzyMatch(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "say \u201chi\u201d   \n"})
	result := run(t, pick(t, cwd, "edit"),
		`{"path":"a.txt","edits":[{"oldText":"say \"hi\"","newText":"say bye"}]}`)

	if got := text(t, result); !strings.HasPrefix(got, "Successfully replaced") {
		t.Fatalf("edit returned %q", got)
	}
	if got := readFile(t, cwd, "a.txt"); got != "say bye\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditErrors(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		args     string
		wantPart string
	}{
		{
			name:     "not found",
			content:  "alpha\n",
			args:     `{"path":"a.txt","edits":[{"oldText":"missing","newText":"x"}]}`,
			wantPart: "Could not find the exact text in a.txt. The old text must match exactly including all whitespace and newlines.",
		},
		{
			name:     "not found among many",
			content:  "alpha\n",
			args:     `{"path":"a.txt","edits":[{"oldText":"alpha","newText":"a"},{"oldText":"missing","newText":"x"}]}`,
			wantPart: "Could not find edits[1] in a.txt.",
		},
		{
			name:     "duplicate",
			content:  "alpha alpha\n",
			args:     `{"path":"a.txt","edits":[{"oldText":"alpha","newText":"beta"}]}`,
			wantPart: "Found 2 occurrences of the text in a.txt. The text must be unique.",
		},
		{
			name:     "overlap",
			content:  "abcdef\n",
			args:     `{"path":"a.txt","edits":[{"oldText":"abc","newText":"x"},{"oldText":"cde","newText":"y"}]}`,
			wantPart: "edits[0] and edits[1] overlap in a.txt.",
		},
		{
			name:     "no change",
			content:  "alpha\n",
			args:     `{"path":"a.txt","edits":[{"oldText":"alpha","newText":"alpha"}]}`,
			wantPart: "No changes made to a.txt. The replacement produced identical content.",
		},
		{
			name:     "empty oldText",
			content:  "alpha\n",
			args:     `{"path":"a.txt","edits":[{"oldText":"","newText":"x"}]}`,
			wantPart: "oldText must not be empty in a.txt.",
		},
		{
			name:     "no edits",
			content:  "alpha\n",
			args:     `{"path":"a.txt","edits":[]}`,
			wantPart: "Edit tool input is invalid. edits must contain at least one replacement.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := newCwd(t, map[string]string{"a.txt": test.content})
			err := runError(t, pick(t, cwd, "edit"), test.args)
			if !strings.Contains(err.Error(), test.wantPart) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantPart)
			}
			// A failed edit must leave the file untouched.
			if got := readFile(t, cwd, "a.txt"); got != test.content {
				t.Fatalf("failed edit modified the file: %q", got)
			}
		})
	}
}

func TestEditMissingFile(t *testing.T) {
	cwd := newCwd(t, map[string]string{})
	err := runError(t, pick(t, cwd, "edit"),
		`{"path":"nope.txt","edits":[{"oldText":"a","newText":"b"}]}`)
	if !strings.Contains(err.Error(), "Could not edit file: nope.txt. Error code: ENOENT.") {
		t.Fatalf("error = %q", err)
	}
}

// A CRLF file must keep CRLF, and a byte order mark must survive the rewrite.
func TestEditPreservesLineEndingsAndBOM(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "\ufeffalpha\r\nbeta\r\n"})
	run(t, pick(t, cwd, "edit"),
		`{"path":"a.txt","edits":[{"oldText":"beta","newText":"gamma"}]}`)

	if got := readFile(t, cwd, "a.txt"); got != "\ufeffalpha\r\ngamma\r\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditDetails(t *testing.T) {
	cwd := newCwd(t, map[string]string{"main.go": "a\nb\nc\n"})
	result := run(t, pick(t, cwd, "edit"),
		`{"path":"main.go","edits":[{"oldText":"b","newText":"B"}]}`)

	var details editDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if !strings.Contains(details.Diff, "-2 b") || !strings.Contains(details.Diff, "+2 B") {
		t.Fatalf("details.diff = %q", details.Diff)
	}
	if !strings.Contains(details.Patch, "--- main.go") || !strings.Contains(details.Patch, "+B") {
		t.Fatalf("details.patch = %q", details.Patch)
	}
	if details.FirstChangedLine == nil || *details.FirstChangedLine != 2 {
		t.Fatalf("firstChangedLine = %v, want 2", details.FirstChangedLine)
	}
}

// The mutation queue exists so two edits of one file cannot each read the
// original content and clobber the other. Eight concurrent edits must all land.
func TestEditSerializesConcurrentMutations(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&builder, "t%d\n", i)
	}
	cwd := newCwd(t, map[string]string{"a.txt": builder.String()})
	impl := pick(t, cwd, "edit")

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf(`{"path":"a.txt","edits":[{"oldText":"t%d","newText":"T%d"}]}`, i, i)
			_, err := impl.Execute(t.Context(), tool.Call{
				ID:        fmt.Sprintf("call-%d", i),
				Name:      "edit",
				Arguments: []byte(args),
			}, func(tool.Partial) {})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent edit failed: %v", err)
	}

	content := readFile(t, cwd, "a.txt")
	for i := 0; i < 8; i++ {
		if !strings.Contains(content, fmt.Sprintf("T%d", i)) {
			t.Fatalf("edit %d was lost, content is %q", i, content)
		}
	}
}

func TestEditKeepsFilePermissions(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "alpha\n"})
	path := filepath.Join(cwd, "a.txt")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	run(t, pick(t, cwd, "edit"), `{"path":"a.txt","edits":[{"oldText":"alpha","newText":"beta"}]}`)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("permissions = %o, want 640", got)
	}
}

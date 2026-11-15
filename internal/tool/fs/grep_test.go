package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrep(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		"main.go":      "package main\n\nfunc main() {\n\tprintln(\"hit\")\n}\n",
		"src/app.go":   "package app\n\nfunc run() {\n\tprintln(\"hit\")\n}\n",
		"src/notes.md": "a hit in prose\n",
	})

	result := run(t, pick(t, cwd, "grep"), `{"pattern":"hit","glob":"*.go"}`)
	got := text(t, result)

	if !strings.Contains(got, "main.go:4: \tprintln(\"hit\")") {
		t.Fatalf("grep output is missing the root match:\n%s", got)
	}
	if !strings.Contains(got, "src/app.go:4: \tprintln(\"hit\")") {
		t.Fatalf("grep output is missing the nested match:\n%s", got)
	}
	if strings.Contains(got, "notes.md") {
		t.Fatalf("glob filter did not exclude notes.md:\n%s", got)
	}
}

func TestGrepNoMatches(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "nothing here\n"})
	result := run(t, pick(t, cwd, "grep"), `{"pattern":"absent"}`)

	if got := text(t, result); got != "No matches found" {
		t.Fatalf("grep returned %q", got)
	}
}

func TestGrepIgnoreCaseAndLiteral(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "Hello there\nHELLO again\na.b dot\n"})

	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"hello","ignoreCase":true}`))
	if !strings.Contains(got, "a.txt:1: Hello there") || !strings.Contains(got, "a.txt:2: HELLO again") {
		t.Fatalf("ignoreCase output:\n%s", got)
	}

	// Without literal, "." matches any character and finds both lines.
	got = text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"a.b"}`))
	if !strings.Contains(got, "a.txt:3: a.b dot") {
		t.Fatalf("regex output:\n%s", got)
	}

	// With literal, "." is just a dot and the regex-only line drops out.
	cwd2 := newCwd(t, map[string]string{"a.txt": "axb\n"})
	got = text(t, run(t, pick(t, cwd2, "grep"), `{"pattern":"a.b","literal":true}`))
	if got != "No matches found" {
		t.Fatalf("literal output: %q", got)
	}
}

func TestGrepContext(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "one\ntwo\nTARGET\nfour\nfive\n"})
	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"TARGET","context":1}`))

	want := "a.txt-2- two\na.txt:3: TARGET\na.txt-4- four"
	if got != want {
		t.Fatalf("grep context output:\n%q\nwant:\n%q", got, want)
	}
}

func TestGrepLimitAndNotice(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "hit\nhit\nhit\nhit\n"})
	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"hit","limit":2}`))

	if strings.Count(got, "a.txt:") != 2 {
		t.Fatalf("limit was not applied:\n%s", got)
	}
	if !strings.Contains(got, "[2 matches limit reached. Use limit=4 for more, or refine pattern]") {
		t.Fatalf("missing match limit notice:\n%s", got)
	}
}

func TestGrepTruncatesLongLines(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "needle " + strings.Repeat("x", 600) + "\n"})
	result := run(t, pick(t, cwd, "grep"), `{"pattern":"needle"}`)
	got := text(t, result)

	if !strings.Contains(got, "... [truncated]") {
		t.Fatalf("long line was not truncated:\n%s", got)
	}
	if !strings.Contains(got, "[Some lines truncated to 500 chars. Use read tool to see full lines]") {
		t.Fatalf("missing line truncation notice:\n%s", got)
	}

	var details grepDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if !details.LinesTruncated {
		t.Fatal("details.linesTruncated should be set")
	}
}

func TestGrepSingleFile(t *testing.T) {
	cwd := newCwd(t, map[string]string{"src/a.go": "target\n"})
	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"target","path":"src/a.go"}`))

	if got != "a.go:1: target" {
		t.Fatalf("searching one file returned %q", got)
	}
}

func TestGrepSkipsBinaries(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.bin": "needle\x00binary\n"})
	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"needle"}`))

	if got != "No matches found" {
		t.Fatalf("binary file should be skipped, got %q", got)
	}
}

func TestGrepRespectsGitignore(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		".gitignore":        "vendor/\n",
		"main.go":           "needle\n",
		"vendor/lib/lib.go": "needle\n",
	})
	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"needle"}`))

	if strings.Contains(got, "vendor/") {
		t.Fatalf("ignored directory leaked into grep results:\n%s", got)
	}
	if !strings.Contains(got, "main.go:1: needle") {
		t.Fatalf("grep output:\n%s", got)
	}
}

func TestGrepErrors(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "x"})

	err := runError(t, pick(t, cwd, "grep"), `{"pattern":"x","path":"missing"}`)
	if !strings.Contains(err.Error(), "Path not found: ") {
		t.Fatalf("error = %q", err)
	}

	err = runError(t, pick(t, cwd, "grep"), `{"pattern":"[unclosed"}`)
	if !strings.Contains(err.Error(), "Invalid pattern") {
		t.Fatalf("error = %q", err)
	}

	runError(t, pick(t, cwd, "grep"), `{}`)
}

func TestGrepUnreadableDirectoryEntryIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	cwd := newCwd(t, map[string]string{
		"visible.txt":       "needle\n",
		"locked/secret.txt": "needle\n",
	})
	if err := os.Chmod(filepath.Join(cwd, "locked"), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(cwd, "locked"), 0o755) })

	got := text(t, run(t, pick(t, cwd, "grep"), `{"pattern":"needle"}`))
	if !strings.Contains(got, "visible.txt:1: needle") {
		t.Fatalf("an unreadable directory should not fail the search:\n%s", got)
	}
}

package fs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		"main.go":               "package main",
		"src/app.go":            "package app",
		"src/deep/util.go":      "package util",
		"src/deep/util_test.go": "package util",
		"docs/readme.md":        "# readme",
	})

	tests := []struct {
		name string
		args string
		want []string
	}{
		{"basename pattern", `{"pattern":"*.go"}`, []string{
			"main.go", "src/app.go", "src/deep/util.go", "src/deep/util_test.go"}},
		{"path pattern", `{"pattern":"src/**/*.go"}`, []string{
			"src/app.go", "src/deep/util.go", "src/deep/util_test.go"}},
		{"single level", `{"pattern":"src/*.go"}`, []string{"src/app.go"}},
		{"other extension", `{"pattern":"*.md"}`, []string{"docs/readme.md"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := run(t, pick(t, cwd, "find"), test.args)
			got := strings.Split(text(t, result), "\n")
			if len(got) != len(test.want) {
				t.Fatalf("find returned %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("find returned %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestFindNoMatches(t *testing.T) {
	cwd := newCwd(t, map[string]string{"main.go": "x"})
	result := run(t, pick(t, cwd, "find"), `{"pattern":"*.rs"}`)

	if got := text(t, result); got != "No files found matching pattern" {
		t.Fatalf("find returned %q", got)
	}
}

// Ignored directories are pruned, so a pattern that would match inside them
// finds nothing, and .git is skipped unconditionally.
func TestFindRespectsGitignore(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		".gitignore":                "node_modules\ndist/\n",
		"main.go":                   "x",
		"node_modules/pkg/index.js": "x",
		"dist/bundle.go":            "x",
		".git/objects/ab":           "x",
		"src/app.go":                "x",
	})

	result := run(t, pick(t, cwd, "find"), `{"pattern":"*.go"}`)
	got := strings.Split(text(t, result), "\n")
	for _, line := range got {
		if strings.Contains(line, "node_modules") || strings.Contains(line, "dist/") || strings.HasPrefix(line, ".git/") {
			t.Fatalf("ignored path leaked into results: %v", got)
		}
	}
	if len(got) != 2 || got[0] != "main.go" || got[1] != "src/app.go" {
		t.Fatalf("find returned %v, want [main.go src/app.go]", got)
	}
}

// A negated rule inside an ignored directory cannot rescue a file, which is what
// git does too: the directory itself is pruned before its files are considered.
func TestFindIgnoreNegation(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		".gitignore":   "*.log\n!keep.log\n",
		"a.log":        "x",
		"keep.log":     "x",
		"src/b.log":    "x",
		"src/keep.log": "x",
	})

	result := run(t, pick(t, cwd, "find"), `{"pattern":"*.log"}`)
	got := strings.Split(text(t, result), "\n")
	if len(got) != 2 || got[0] != "keep.log" || got[1] != "src/keep.log" {
		t.Fatalf("find returned %v, want the un-ignored files", got)
	}
}

func TestFindLimit(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go"} {
		files[name] = "x"
	}
	cwd := newCwd(t, files)

	result := run(t, pick(t, cwd, "find"), `{"pattern":"*.go","limit":2}`)
	got := text(t, result)
	if !strings.HasPrefix(got, "a.go\nb.go") {
		t.Fatalf("find with a limit returned %q", got)
	}
	if !strings.Contains(got, "[2 results limit reached. Use limit=4 for more, or refine pattern]") {
		t.Fatalf("missing result limit notice: %q", got)
	}

	var details findDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.ResultLimitReached != 2 {
		t.Fatalf("resultLimitReached = %d", details.ResultLimitReached)
	}
}

func TestFindSearchesSubdirectory(t *testing.T) {
	cwd := newCwd(t, map[string]string{
		"root.go":         "x",
		"src/app.go":      "x",
		"src/deep/lib.go": "x",
	})
	result := run(t, pick(t, cwd, "find"), `{"pattern":"*.go","path":"src"}`)

	got := strings.Split(text(t, result), "\n")
	if len(got) != 2 || got[0] != "app.go" || got[1] != "deep/lib.go" {
		t.Fatalf("find in a subdirectory returned %v", got)
	}
}

func TestFindErrors(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "x"})

	err := runError(t, pick(t, cwd, "find"), `{"pattern":"*.go","path":"missing"}`)
	if !strings.Contains(err.Error(), "Path not found: ") {
		t.Fatalf("error = %q", err)
	}

	runError(t, pick(t, cwd, "find"), `{}`)
}

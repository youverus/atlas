package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDeriveName(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"empty", "", ""},
		{"blank only", " \n\t\n ", ""},
		{"short line", "hi", "hi"},
		// The example this was written for: the whole message is one breath
		// long, and the first clause names it.
		{
			"first clause in Chinese",
			"用 read 工具读 go.mod，只回答第一行的 module 名称，不要做别的",
			"用 read 工具读 go.mod",
		},
		// A clause shorter than minDerivedClause says nothing on its own, so
		// the whole line is the better name.
		{"clause too short to keep", "好的，帮我跑一下测试", "好的，帮我跑一下测试"},
		{"first line wins", "fix the parser\n\nit breaks on a trailing comma", "fix the parser"},
		{"leading newlines are skipped", "\n\n  ship the release  \nmore detail", "ship the release"},
		{"heading mark", "# Release notes", "Release notes"},
		{"bullet mark", "- fix the flaky test", "fix the flaky test"},
		// A clause break ends the name, and the punctuation that ended it is
		// noise in a label.
		{"quote mark", "> why does this fail?", "why does this fail"},
		// The fence line names a language; the line under it is the content.
		{"code fence", "```go\nfmt.Println(\"hi\")\n```", "fmt.Println(\"hi\")"},
		{"blanks fold", "fix  \t the    parser", "fix the parser"},
		{"no clause break", "explain the projection cache", "explain the projection cache"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DeriveName(testCase.text); got != testCase.want {
				t.Fatalf("DeriveName(%q) = %q, want %q", testCase.text, got, testCase.want)
			}
		})
	}
}

func TestDeriveNameCutsLongTextAtAWordBoundary(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("parser ", 20))
	got := DeriveName(text)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long name %q does not end in an ellipsis", got)
	}
	if strings.Contains(got, "pars…") {
		t.Fatalf("long name %q cut inside a word", got)
	}
	if runes := utf8.RuneCountInString(got); runes > maxDerivedName+1 {
		t.Fatalf("long name is %d runes, want at most %d", runes, maxDerivedName+1)
	}
}

// Text written without blanks has no word boundary to back off to, so the cut
// lands where the budget ends instead of shortening the name for nothing.
func TestDeriveNameCutsUnbrokenTextAtTheBudget(t *testing.T) {
	text := strings.Repeat("测", 100)
	got := DeriveName(text)
	if want := strings.Repeat("测", maxDerivedName) + "…"; got != want {
		t.Fatalf("DeriveName(long Chinese) = %q, want %q", got, want)
	}
}

// A name is written into the session file, so it must survive the same
// sanitizing a hand-typed one does.
func TestDeriveNameSurvivesSanitize(t *testing.T) {
	text := "  plan\n\nreview the diff  "
	got := DeriveName(text)
	if got != "plan" {
		t.Fatalf("DeriveName(%q) = %q, want %q", text, got, "plan")
	}
	if sanitized := sanitizeName(got); sanitized != got {
		t.Fatalf("sanitizeName(%q) = %q, want it unchanged", got, sanitized)
	}
}

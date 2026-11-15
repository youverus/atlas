package fs

import (
	"strings"
	"testing"
)

func TestDiffLines(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		// want is a compact notation: "=lines", "-lines", "+lines".
		want []string
	}{
		{
			name: "identical",
			old:  "a\nb\n",
			new:  "a\nb\n",
			want: []string{"=a,b"},
		},
		{
			name: "pure insertion",
			old:  "a\nc\n",
			new:  "a\nb\nc\n",
			want: []string{"=a", "+b", "=c"},
		},
		{
			name: "pure deletion",
			old:  "a\nb\nc\n",
			new:  "a\nc\n",
			want: []string{"=a", "-b", "=c"},
		},
		{
			name: "replacement",
			old:  "a\nb\nc\n",
			new:  "a\nB\nc\n",
			want: []string{"=a", "-b", "+B", "=c"},
		},
		{
			name: "prefix and suffix reused",
			old:  "one\ntwo\nthree\nfour\n",
			new:  "one\nTWO\nthree\nfour\n",
			want: []string{"=one", "-two", "+TWO", "=three,four"},
		},
		{
			name: "append at end",
			old:  "a\n",
			new:  "a\nb\n",
			want: []string{"=a", "+b"},
		},
		{
			name: "empty old",
			old:  "",
			new:  "a\nb\n",
			want: []string{"+a,b"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := diffLines(test.old, test.new)
			var got []string
			for _, part := range parts {
				prefix := "="
				if part.added {
					prefix = "+"
				} else if part.removed {
					prefix = "-"
				}
				got = append(got, prefix+strings.Join(part.lines, ","))
			}
			if len(got) != len(test.want) {
				t.Fatalf("diffLines = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("diffLines = %v, want %v", got, test.want)
				}
			}
		})
	}
}

// Reconstructing one side from the diff must give back that side exactly. This
// checks that no line is lost or duplicated by the Myers backtrack.
func TestDiffLinesRoundTrip(t *testing.T) {
	cases := []struct{ old, new string }{
		{"a\nb\nc\nd\ne\n", "a\nX\nc\nY\ne\n"},
		{"1\n2\n3\n", "3\n2\n1\n"},
		{"same\n", "same\n"},
		{"a\nb\nc\n", "a\nb\nc\nd\ne\nf\n"},
		{"x\ny\nz\n", ""},
	}
	for _, test := range cases {
		parts := diffLines(test.old, test.new)
		var oldBack, newBack []string
		for _, part := range parts {
			if !part.added {
				oldBack = append(oldBack, part.lines...)
			}
			if !part.removed {
				newBack = append(newBack, part.lines...)
			}
		}
		if got := strings.Join(oldBack, "\n"); got != strings.TrimSuffix(test.old, "\n") {
			t.Fatalf("old side reconstructed as %q from %q", got, test.old)
		}
		if got := strings.Join(newBack, "\n"); got != strings.TrimSuffix(test.new, "\n") {
			t.Fatalf("new side reconstructed as %q from %q", got, test.new)
		}
	}
}

// A change larger than the Myers budget still produces a correct, if coarser,
// diff.
func TestDiffLinesLargeChange(t *testing.T) {
	var oldLines, newLines []string
	for i := 0; i < 900; i++ {
		oldLines = append(oldLines, "old line")
		newLines = append(newLines, "new line")
	}
	parts := diffLines(strings.Join(oldLines, "\n"), strings.Join(newLines, "\n"))

	removed, added := 0, 0
	for _, part := range parts {
		if part.removed {
			removed += len(part.lines)
		}
		if part.added {
			added += len(part.lines)
		}
	}
	if removed != 900 || added != 900 {
		t.Fatalf("coarse diff lost lines: removed %d, added %d", removed, added)
	}
}

func TestGenerateDiffString(t *testing.T) {
	old := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	updated := "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\nnine\nten\n"

	diff, firstChangedLine := generateDiffString(old, updated, 2)
	if firstChangedLine != 5 {
		t.Fatalf("firstChangedLine = %d, want 5", firstChangedLine)
	}
	if !strings.Contains(diff, "- 5 five") || !strings.Contains(diff, "+ 5 FIVE") {
		t.Fatalf("diff is missing the change:\n%s", diff)
	}
	// Distant context must be replaced by a gap marker.
	if !strings.Contains(diff, "...") {
		t.Fatalf("diff should elide distant context:\n%s", diff)
	}
	if strings.Contains(diff, "ten") {
		t.Fatalf("diff should not include the far end of the file:\n%s", diff)
	}
}

func TestGenerateDiffStringNoContext(t *testing.T) {
	diff, firstChangedLine := generateDiffString("a\nb\n", "a\nb\n", 4)
	if diff != "" {
		t.Fatalf("an unchanged file should produce an empty diff, got %q", diff)
	}
	if firstChangedLine != 0 {
		t.Fatalf("firstChangedLine = %d, want 0", firstChangedLine)
	}
}

func TestGenerateUnifiedPatch(t *testing.T) {
	patch := generateUnifiedPatch("src/main.go", "a\nb\nc\n", "a\nB\nc\n", 1)

	for _, want := range []string{
		"--- src/main.go\n",
		"+++ src/main.go\n",
		"@@ -1,3 +1,3 @@\n",
		" a\n",
		"-b\n",
		"+B\n",
		" c\n",
	} {
		if !strings.Contains(patch, want) {
			t.Fatalf("patch is missing %q:\n%s", want, patch)
		}
	}
}

// Applying a fuzzy match must not rewrite lines the model did not touch. The
// touched line is re-emitted from the normalized base (so its own trailing
// whitespace is gone, which is what pi does too), while every other line keeps
// its original bytes.
func TestApplyReplacementsPreservingUnchangedLines(t *testing.T) {
	original := "keep   \ntarget\t\nkeep2  \n"
	base := normalizeForFuzzyMatch(original)

	index := strings.Index(base, "target")
	if index < 0 {
		t.Fatalf("fuzzy base lost the target: %q", base)
	}
	got, err := applyReplacementsPreservingUnchangedLines(original, base,
		[]replacement{{matchIndex: index, matchLength: len("target"), newText: "TARGET"}})
	if err != nil {
		t.Fatalf("applyReplacementsPreservingUnchangedLines: %v", err)
	}
	if got != "keep   \nTARGET\nkeep2  \n" {
		t.Fatalf("preservation is wrong: %q", got)
	}
}

func TestApplyReplacementsOrderIndependent(t *testing.T) {
	// Two disjoint replacements applied from the end so the first offset stays
	// valid.
	got := applyReplacements("a-b-c", []replacement{
		{matchIndex: 4, matchLength: 1, newText: "C"},
		{matchIndex: 0, matchLength: 1, newText: "A"},
	}, 0)
	if got != "A-b-C" {
		t.Fatalf("applyReplacements = %q", got)
	}
}

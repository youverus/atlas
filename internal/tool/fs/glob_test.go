package fs

import "testing"

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		rel     string
		want    bool
	}{
		// A pattern without a slash matches the base name at any depth, which is
		// how fd behaves without --full-path.
		{"basename at root", "*.ts", "main.ts", true},
		{"basename nested", "*.ts", "src/deep/main.ts", true},
		{"basename mismatch", "*.ts", "src/main.tsx", false},
		{"dotfile", ".gitignore", ".gitignore", true},
		{"question mark", "a?.go", "src/ab.go", true},
		{"character class", "[abc].go", "b.go", true},
		{"class miss", "[abc].go", "d.go", false},

		// A pattern with a slash is anchored at the search root, but fd also
		// finds the same shape deeper down.
		{"anchored exact", "src/*.ts", "src/main.ts", true},
		{"anchored deep", "src/*.ts", "src/nested/main.ts", false},
		{"implicit any depth", "src/*.ts", "app/src/main.ts", true},
		{"leading slash pins root", "/src/*.ts", "src/main.ts", true},
		{"leading slash rejects depth", "/src/*.ts", "app/src/main.ts", false},

		// Asterisk stops at a separator; double asterisk crosses it.
		{"single star one level", "src/*/*.ts", "src/a/b.ts", true},
		{"single star not two levels", "src/*/*.ts", "src/a/b/c.ts", false},
		{"double star spans", "src/**/*.ts", "src/a/b/c.ts", true},
		{"double star zero segments", "src/**/*.ts", "src/main.ts", true},
		{"trailing double star", "src/**", "src/a/b.ts", true},
		{"everything", "**", "a/b/c.ts", true},

		// Braces expand into alternatives.
		{"braces first", "*.{ts,tsx}", "a.ts", true},
		{"braces second", "*.{ts,tsx}", "a.tsx", true},
		{"braces miss", "*.{ts,tsx}", "a.js", false},
		{"braces with path", "src/*.{go,rs}", "src/main.rs", true},

		{"leading dot slash", "./src/*.ts", "src/a.ts", true},
		{"empty pattern", "", "a.ts", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := matchPattern(test.pattern, test.rel); got != test.want {
				t.Fatalf("matchPattern(%q, %q) = %v, want %v", test.pattern, test.rel, got, test.want)
			}
		})
	}
}

func TestExpandBraces(t *testing.T) {
	tests := []struct {
		pattern string
		want    []string
	}{
		{"a.ts", []string{"a.ts"}},
		{"*.{ts,tsx}", []string{"*.ts", "*.tsx"}},
		{"{a,b,c}", []string{"a", "b", "c"}},
		{"src/{a,b}/*.go", []string{"src/a/*.go", "src/b/*.go"}},
		// Nested braces are left alone rather than half-expanded.
		{"{a,{b,c}}", []string{"{a,{b,c}}"}},
		// An unclosed brace is literal.
		{"{a,b", []string{"{a,b"}},
	}

	for _, test := range tests {
		t.Run(test.pattern, func(t *testing.T) {
			got := expandBraces(test.pattern)
			if len(got) != len(test.want) {
				t.Fatalf("expandBraces(%q) = %v, want %v", test.pattern, got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("expandBraces(%q) = %v, want %v", test.pattern, got, test.want)
				}
			}
		})
	}
}

package fs

import (
	"os"
	"testing"
)

func TestParseIgnoreLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantOK   bool
		pattern  string
		negate   bool
		dirOnly  bool
		anchored bool
	}{
		{"comment", "# build output", false, "", false, false, false},
		{"blank", "", false, "", false, false, false},
		{"plain", "node_modules", true, "node_modules", false, false, false},
		{"negation", "!keep.txt", true, "keep.txt", true, false, false},
		{"directory only", "dist/", true, "dist", false, true, false},
		{"anchored", "src/generated", true, "src/generated", false, false, true},
		{"anchored glob", "/src/*.gen.go", true, "src/*.gen.go", false, false, true},
		{"trailing spaces dropped", "build   ", true, "build", false, false, false},
		{"leading dot slash", "./build", true, "build", false, false, false},
		{"carriage return", "build\r", true, "build", false, false, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, ok := parseIgnoreLine(test.line)
			if ok != test.wantOK {
				t.Fatalf("parseIgnoreLine(%q) ok = %v, want %v", test.line, ok, test.wantOK)
			}
			if !ok {
				return
			}
			if rule.pattern != test.pattern || rule.negate != test.negate ||
				rule.dirOnly != test.dirOnly || rule.anchored != test.anchored {
				t.Fatalf("parseIgnoreLine(%q) = %+v", test.line, rule)
			}
		})
	}
}

func TestIgnoreStackIgnored(t *testing.T) {
	root := newCwd(t, map[string]string{
		".gitignore": "node_modules\ndist/\n*.log\n!keep.log\n/src/generated\n",
	})
	stack := newIgnoreStack()
	pushed := stack.push(root, "", os.ReadFile)
	if pushed != 1 {
		t.Fatalf("expected one level pushed, got %d", pushed)
	}

	tests := []struct {
		name  string
		rel   string
		isDir bool
		want  bool
	}{
		{"plain name at depth", "packages/app/node_modules", true, true},
		{"file inside ignored dir", "packages/app/node_modules/index.js", false, false},
		{"directory rule on dir", "dist", true, true},
		{"directory rule on file", "dist", false, false},
		{"extension rule", "logs/build.log", false, true},
		{"negated", "logs/keep.log", false, false},
		{"anchored hit", "src/generated", true, true},
		{"anchored miss", "app/src/generated", true, false},
		{"untouched", "src/main.go", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stack.ignored(test.rel, test.isDir); got != test.want {
				t.Fatalf("ignored(%q, %v) = %v, want %v", test.rel, test.isDir, got, test.want)
			}
		})
	}
}

// A nested .gitignore overrides its parent, and popping it restores the parent
// rules. This is the behavior the walker depends on when it descends and then
// returns.
func TestIgnoreStackNestedPrecedence(t *testing.T) {
	root := newCwd(t, map[string]string{
		".gitignore":     "*.log\n",
		"sub/.gitignore": "!important.log\n",
	})
	stack := newIgnoreStack()
	stack.push(root, "", os.ReadFile)

	if !stack.ignored("a.log", false) {
		t.Fatal("parent rule should ignore a.log")
	}

	sub := root + "/sub"
	level := stack.push(sub, "sub", os.ReadFile)
	if level != 1 {
		t.Fatalf("expected the nested level to be pushed, got %d", level)
	}
	// The nested negation wins for its own subtree only.
	if stack.ignored("sub/important.log", false) {
		t.Fatal("nested negation should un-ignore sub/important.log")
	}
	if !stack.ignored("important.log", false) {
		t.Fatal("nested rules must not apply outside their directory")
	}

	stack.pop(level)
	if !stack.ignored("sub/important.log", false) {
		t.Fatal("popping the nested level should restore the parent rule")
	}
}

func TestIgnoreStackMissingFile(t *testing.T) {
	root := newCwd(t, map[string]string{"a.txt": "x"})
	stack := newIgnoreStack()
	if pushed := stack.push(root, "", os.ReadFile); pushed != 0 {
		t.Fatalf("a directory without .gitignore should push nothing, got %d", pushed)
	}
	if stack.ignored("a.txt", false) {
		t.Fatal("nothing should be ignored without rules")
	}
}

package fs

import (
	"bufio"
	"path"
	"strings"
)

// ignoreStack holds the .gitignore rules that apply to the directory currently
// being walked. pi delegates this to fd and ripgrep; a single static binary has
// to implement it, so the subset git actually uses in practice is here.
//
// Supported: "#" comments, "!" negation, a trailing "/" for directory-only
// rules, anchoring when the pattern contains "/", "*" that stops at "/", "**"
// that spans segments, "?" and character classes.
//
// Not supported, and harmless to omit: global excludes files,
// .git/info/exclude, .ignore files, and backslash escapes.
type ignoreStack struct {
	levels []ignoreLevel
}

// ignoreLevel is one .gitignore file. prefix is the directory of that file
// relative to the walk root, so rules can be matched against paths relative to
// the walk root.
type ignoreLevel struct {
	prefix string
	rules  []ignoreRule
}

type ignoreRule struct {
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool
}

func newIgnoreStack() *ignoreStack {
	return &ignoreStack{}
}

// push reads dir/.gitignore and adds its rules. It returns how many levels were
// added so the walker can undo exactly that.
func (s *ignoreStack) push(dir, prefix string, readFile func(string) ([]byte, error)) int {
	data, err := readFile(joinAccessPath(dir, ".gitignore"))
	if err != nil {
		return 0
	}
	rules := parseIgnoreFile(string(data))
	if len(rules) == 0 {
		return 0
	}
	s.levels = append(s.levels, ignoreLevel{prefix: prefix, rules: rules})
	return 1
}

// pop removes the last n levels.
func (s *ignoreStack) pop(n int) {
	if n <= 0 {
		return
	}
	if n >= len(s.levels) {
		s.levels = s.levels[:0]
		return
	}
	s.levels = s.levels[:len(s.levels)-n]
}

// ignored reports whether the path, given relative to the walk root with
// forward slashes, is excluded. Deeper .gitignore files take precedence over
// shallower ones, and within one file the last matching rule wins.
func (s *ignoreStack) ignored(rel string, isDir bool) bool {
	result := false
	for _, level := range s.levels {
		ruleRel, ok := stripPrefix(rel, level.prefix)
		if !ok {
			continue
		}
		for _, rule := range level.rules {
			if rule.matches(ruleRel, isDir) {
				result = !rule.negate
			}
		}
	}
	return result
}

// stripPrefix removes the level prefix, reporting false when the path is not
// below it.
func stripPrefix(rel, prefix string) (string, bool) {
	if prefix == "" {
		return rel, true
	}
	if rel == prefix {
		return "", true
	}
	if !strings.HasPrefix(rel, prefix+"/") {
		return "", false
	}
	return rel[len(prefix)+1:], true
}

func (r ignoreRule) matches(rel string, isDir bool) bool {
	if rel == "" {
		return false
	}
	if r.dirOnly && !isDir {
		return false
	}
	if !r.anchored {
		base := rel
		if index := strings.LastIndex(rel, "/"); index >= 0 {
			base = rel[index+1:]
		}
		ok, err := path.Match(r.pattern, base)
		return err == nil && ok
	}
	return matchSegments(splitPath(r.pattern), splitPath(rel))
}

func parseIgnoreFile(content string) []ignoreRule {
	var rules []ignoreRule
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if rule, ok := parseIgnoreLine(scanner.Text()); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

func parseIgnoreLine(line string) (ignoreRule, bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	// Unescaped trailing spaces are not part of the pattern. Escapes are not
	// supported, so a trailing backslash-space is treated as a plain space.
	line = strings.TrimRight(line, " ")

	rule := ignoreRule{}
	if strings.HasPrefix(line, "!") {
		rule.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		rule.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	line = strings.TrimPrefix(line, "./")
	if line == "" {
		return ignoreRule{}, false
	}
	// A leading "/" anchors the rule to the .gitignore's own directory. It is
	// not part of the pattern, so it is dropped before matching.
	leadingSlash := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return ignoreRule{}, false
	}
	rule.pattern = line
	rule.anchored = leadingSlash || strings.Contains(line, "/")
	return rule, true
}

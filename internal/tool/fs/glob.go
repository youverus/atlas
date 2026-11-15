package fs

import (
	"path"
	"strings"
)

// matchPattern reports whether a slash-separated relative path matches a glob
// pattern.
//
// The rules follow fd, which pi shells out to:
//   - a pattern without "/" matches the base name at any depth
//   - a pattern with "/" matches from the search root, but may also match
//     deeper occurrences, so "src/*.ts" finds "app/src/a.ts"
//   - "**" matches any number of segments, including none
//   - "{a,b}" expands into alternatives
func matchPattern(pattern, rel string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	// An empty pattern matches nothing: it is not a wildcard for "everything".
	if pattern == "" {
		return false
	}
	if pattern == "**" {
		return rel != ""
	}
	base := rel
	if index := strings.LastIndex(rel, "/"); index >= 0 {
		base = rel[index+1:]
	}

	anchored := strings.Contains(pattern, "/")
	if !anchored {
		for _, expanded := range expandBraces(pattern) {
			if ok, err := path.Match(expanded, base); err == nil && ok {
				return true
			}
		}
		return false
	}

	segments := splitPath(rel)
	for _, expanded := range expandBraces(pattern) {
		patternSegments := splitPath(strings.TrimPrefix(expanded, "/"))
		if matchSegments(patternSegments, segments) {
			return true
		}
		// Anchor the pattern at any depth, mirroring fd's implicit "**/" for
		// path patterns.
		if !strings.HasPrefix(expanded, "/") {
			if matchSegments(append([]string{"**"}, patternSegments...), segments) {
				return true
			}
		}
	}
	return false
}

// matchSegments matches segment lists, with "**" consuming any number of them.
func matchSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(name); skip++ {
			if matchSegments(pattern[1:], name[skip:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], name[1:])
}

func splitPath(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

// expandBraces turns "a.{ts,tsx}" into ["a.ts", "a.tsx"]. Nested braces are not
// supported and are left untouched, which is what path.Match then treats as a
// literal.
func expandBraces(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	close := strings.IndexByte(pattern[open:], '}')
	if close < 0 {
		return []string{pattern}
	}
	close += open
	prefix, body, suffix := pattern[:open], pattern[open+1:close], pattern[close+1:]
	if strings.Contains(body, "{") {
		return []string{pattern}
	}

	var out []string
	for _, alternative := range strings.Split(body, ",") {
		for _, expanded := range expandBraces(prefix + alternative + suffix) {
			out = append(out, expanded)
		}
	}
	return out
}

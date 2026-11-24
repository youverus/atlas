package skill

import (
	"errors"
	"fmt"
	"strings"
)

// The frontmatter reader is a YAML subset, and it is hand-written because the
// kernel may not import a parser (tests/deps/run.sh). The subset is what a
// SKILL.md actually contains: one-line scalars, | and > blocks with optional
// trailing-newline indicators, comments, and keys nobody reads.
//
// What it does not read, it does not guess at. An anchor, a flow collection or a
// second document is an error with a line number, because a guessed value would
// become the description the model chooses a skill by, and a wrong description
// is worse than no skill: it makes the skill invisible in a way nobody can
// debug.

// errNoFrontmatter keeps "this file is prose" apart from "this file is broken".
// It is what a loose .md file in a skills directory hits, and the difference
// matters: one is a file to ignore, the other is a file to fix.
var errNoFrontmatter = errors.New("no --- frontmatter on the first line, so it is not a skill")

// readKeys are the keys this package uses. Everything else is skipped whole,
// including an indented block under it: metadata: is a map by the specification,
// and walking past it is the difference between ignoring an extension point and
// rejecting a file that is valid.
var readKeys = map[string]bool{
	"name":             true,
	"description":      true,
	"allowed-tools":    true,
	"disallowed-tools": true,
}

// parseFrontmatter returns the top-level keys of a skill's frontmatter.
//
// A file whose first line is not --- is not a skill at all (errNoFrontmatter).
// A file that starts one and never closes it is broken, and says so: the
// alternative is swallowing the whole document as a value no field can hold.
func parseFrontmatter(text string) (map[string]string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if strings.TrimRight(lines[0], " \t") != "---" {
		return nil, errNoFrontmatter
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, errors.New("the --- on line 1 is never closed; the block ends at a line that holds only ---")
	}

	body := lines[1:end]
	values := make(map[string]string, 4)
	for i := 0; i < len(body); i++ {
		line := body[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// The number is the file's, so a message can be acted on without
		// counting from the top of the block.
		number := i + 2
		if line[0] == ' ' || line[0] == '\t' {
			return nil, subsetError(number, "an indented line with no key above it")
		}
		key, rest, ok := splitKey(line)
		if !ok {
			return nil, subsetError(number, fmt.Sprintf("%q is not a key: value pair", line))
		}
		if !readKeys[key] {
			// The key and whatever is nested under it, in one step: a value this
			// reader has no use for cannot be a reason to refuse the file.
			i = skipBlock(body, i+1) - 1
			continue
		}
		switch header := strings.TrimSpace(stripComment(rest)); header {
		case "|", "|-", "|+", ">", ">-", ">+":
			block, next := blockScalar(body, i+1)
			i = next - 1
			// Skill metadata is consumed without trailing whitespace.
			values[key] = fold(block, header[0] == '>')
		case "":
			return nil, subsetError(number, fmt.Sprintf("%s: has no value on its line", key))
		default:
			if err := rejectSyntax(rest); err != nil {
				return nil, subsetError(number, err.Error())
			}
			values[key] = scalar(rest)
			if i+1 < len(body) && (body[i+1] != "" && (body[i+1][0] == ' ' || body[i+1][0] == '\t')) {
				return nil, subsetError(number+1, fmt.Sprintf("an indented block under %s:, which this reader reads as one line", key))
			}
		}
	}
	return values, nil
}

// splitKey splits "key: value" at the first colon. Anything after it is the
// value, which is why this is not a regular expression that takes the first
// line: a description containing a colon ("Use this when: the build fails") is
// the normal case, and cutting it there would produce half a sentence.
func splitKey(line string) (string, string, bool) {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return "", "", false
	}
	key := strings.TrimRight(line[:colon], " \t")
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(line[colon+1:]), true
}

// blockScalar reads the indented lines under "key: |" or "key: >" and returns
// the first line after them.
//
// Blank lines inside the block are content — they are what separates two
// paragraphs of a description — but trailing ones are not: they belong to
// whatever the author put next.
func blockScalar(body []string, start int) ([]string, int) {
	end := start
	for end < len(body) && (body[end] == "" || body[end][0] == ' ' || body[end][0] == '\t') {
		end++
	}
	block := body[start:end]
	for len(block) > 0 && strings.TrimSpace(block[len(block)-1]) == "" {
		block = block[:len(block)-1]
	}
	indent := 0
	for _, line := range block {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if n := len(line) - len(strings.TrimLeft(line, " \t")); indent == 0 || n < indent {
			indent = n
		}
	}
	unindented := make([]string, len(block))
	for i, line := range block {
		if len(line) >= indent {
			unindented[i] = line[indent:]
		}
	}
	return unindented, end
}

// fold joins a block's lines: | keeps them, > folds them. A folded block is one
// line per paragraph, which is what a description written as ">" means.
func fold(lines []string, folded bool) string {
	if !folded {
		return strings.Join(lines, "\n")
	}
	paragraphs := make([]string, 0, 2)
	current := make([]string, 0, 4)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				paragraphs = append(paragraphs, strings.Join(current, " "))
				current = current[:0]
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		paragraphs = append(paragraphs, strings.Join(current, " "))
	}
	return strings.Join(paragraphs, "\n")
}

// skipBlock returns the index of the first line that is neither indented nor
// blank, which is where the next key starts.
func skipBlock(body []string, start int) int {
	end := start
	for end < len(body) && (body[end] == "" || body[end][0] == ' ' || body[end][0] == '\t') {
		end++
	}
	return end
}

// rejectSyntax refuses the three shapes a reader without an implementation would
// silently turn into a value: an anchor, an alias, and a flow collection.
//
// The alternative is worse than refusing the file. "description: &why Use this
// when the build fails" would otherwise be listed with "&why" in it, and the
// model would choose between skills by a string nobody wrote.
func rejectSyntax(value string) error {
	switch {
	case strings.HasPrefix(value, "&"):
		return errors.New("an anchor (&) is not read")
	case strings.HasPrefix(value, "*"):
		return errors.New("an alias (*) is not read")
	case strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]"):
		return errors.New("a flow sequence ([...]) is not read")
	case strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}"):
		return errors.New("a flow mapping ({...}) is not read")
	}
	return nil
}

// scalar strips a trailing comment, the whitespace around the value, and one
// pair of matching quotes.
//
// Escapes inside the quotes are left as they are: a value this reader does not
// unescape is still the value, and inventing an unescape rule would silently
// change a description.
func scalar(rest string) string {
	value := strings.TrimSpace(stripComment(rest))
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if first == last && (first == '"' || first == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// stripComment cuts a " #" comment. A # inside quotes is part of the value: a
// description like "cut the #1234 ticket" is a sentence, not a comment.
func stripComment(value string) string {
	var quote byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && i > 0 && (value[i-1] == ' ' || value[i-1] == '\t'):
			return value[:i]
		}
	}
	return value
}

// subsetError names the line and what the reader does support. "parse error"
// would send someone who wrote valid YAML looking for a specification this
// reader does not implement, so the message says what to write instead.
func subsetError(line int, what string) error {
	return fmt.Errorf("frontmatter line %d: %s; this reader reads one-line values, | and > blocks with optional + or -, and # comments", line, what)
}

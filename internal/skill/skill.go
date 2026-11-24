// Package skill finds the skills available to a session: the metadata the prompt
// lists, and the path of the file the model reads when it wants the rest.
//
// A skill grants nothing. It is text — the same text the user could have typed —
// so the model loads it with the file tool it already has, and a line in a skill
// that says "run this" goes through the approvals and the sandbox a hand-typed
// command goes through. That is why there is no executor here and no skill tool:
// the feature is discovery, one block of prompt, and a file read.
//
// The package does not know where the roots are. The caller names them, which
// keeps the kernel out of the config file and lets a test build a world inside
// one temporary directory. See docs/skills.md.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skillFileName is the file that makes a directory a skill.
const skillFileName = "SKILL.md"

// maxName and maxDescription are the specification's limits. They are enforced
// rather than reported because both are what make a skill findable: a name that
// cannot be typed cannot be called, and a description nobody finishes reading
// describes nothing.
const (
	maxName        = 64
	maxDescription = 1024
)

// Scope is where a skill was found.
//
// It is part of a skill rather than a detail of the scan because it is what
// decides a name collision: the caller lists its most trusted root first, and the
// first root to claim a name keeps it.
type Scope string

const (
	// User is $ATLAS_HOME/skills: the skills of the person, not of a project.
	User Scope = "user"
	// Shared is ~/.agents/skills: the same directory other agents in this
	// family read, so a skill does not have to be copied once per agent.
	Shared Scope = "shared"
	// Plugin is a skills directory inside an installed plugin. It is told apart
	// from User because "which plugin brought this" is the question the reader
	// asks, and the path answers it (docs/architecture.md item 82).
	Plugin Scope = "plugin"
	// Project is <cwd>/.atlas/skills: skills that arrive with a checkout.
	Project Scope = "project"
	// ProjectShared is <cwd>/.agents/skills: skills that arrive with a checkout,
	// in the shared spelling.
	ProjectShared Scope = "project-shared"
)

// Root is one directory to scan and where it sits in the search order.
type Root struct {
	Dir   string
	Scope Scope
}

// Skill is one skill as the prompt needs it: the metadata, and where the body
// is.
//
// The body is not read here on purpose. A session that lists twenty skills pays
// for twenty entries, and a skill that is never used costs nothing else; reading
// the bodies would turn the list into the prompt.
type Skill struct {
	Name        string
	Description string
	// Path is the markdown file: absolute, because it is what the model is told
	// to read, and a relative path would resolve against the wrong directory the
	// moment a tool runs somewhere else.
	Path  string
	Scope Scope
}

// Limits are the point past which a skill's file is longer than the tool that
// reads it will show. A zero field turns that check off.
//
// They are passed in rather than imported because the reader is the file tool,
// which lives above this package: the warning has to quote the number the read
// will actually stop at, and a copy of that number would drift.
type Limits struct {
	MaxBytes int
	MaxLines int
}

// Text is the skill's file, ready to become a message.
//
// The whole file comes back, frontmatter included: that is what the model gets
// when it reads the same path itself, and a second rendering of one file would
// be a second thing to keep true. The limits are checked rather than applied —
// a truncated instruction is the failure this feature exists to avoid, so a file
// that one read cannot show whole is refused, and the refusal names the fix.
func (s Skill) Text(limits Limits) (string, error) {
	content, err := os.ReadFile(s.Path)
	if err != nil {
		return "", err
	}
	text := stripBOM(string(content))
	if why := limits.exceeded(content, text); why != "" {
		return "", errors.New(why)
	}
	return text, nil
}

// Load scans the roots in order and returns the skills it found, plus one
// warning per file that was skipped or accepted with a caveat.
//
// The order is the whole collision rule, so the caller lists its most trusted
// root first: a project cannot replace a skill the user installed without the
// user being told. That is the same rule mcp.json follows, where a project file
// may only disable a server the user's file declared.
//
// A missing root is not a finding. A fresh install has none of these
// directories, and a warning about a directory nobody was told to create would
// train the reader to ignore the warnings that matter.
func Load(roots []Root, limits Limits) ([]Skill, []string) {
	skills := make([]Skill, 0, 8)
	notes := make([]string, 0, 4)
	owner := make(map[string]string, 8)
	seenRoots := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		dir := root.Dir
		if absolute, err := filepath.Abs(dir); err == nil {
			dir = absolute
		}
		if _, seen := seenRoots[dir]; seen {
			continue
		}
		seenRoots[dir] = struct{}{}
		for _, file := range discover(dir) {
			found, foundNotes := loadFile(file, root.Scope, limits)
			notes = append(notes, foundNotes...)
			if found == nil {
				continue
			}
			if winner, taken := owner[found.Name]; taken {
				notes = append(notes, problem(found.Path, "the name %q belongs to %s, which comes earlier in the search order; this skill is ignored", found.Name, winner))
				continue
			}
			owner[found.Name] = found.Path
			skills = append(skills, *found)
		}
	}
	return skills, notes
}

// candidate is one file that might be a skill. Nested says the file is a
// SKILL.md inside a directory of its own, which is what makes the directory name
// part of the skill's identity.
type candidate struct {
	path   string
	nested bool
}

// discover lists the candidates directly under dir: a subdirectory that holds a
// SKILL.md, and every markdown file.
//
// It does not recurse. A skill's own directory holds its references and its
// scripts, so looking deeper would turn one skill into several; and a scan that
// follows directories into a checkout is a scan that finds a generated copy of
// itself. Two levels is exactly "a directory with a SKILL.md in it".
func discover(dir string) []candidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		path := filepath.Join(dir, name)
		// Stat follows symlinks, so a linked skill counts: pointing one directory
		// at another is how a person keeps a skill in one place and uses it from
		// three.
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if markdown, ok := skillFileIn(path); ok {
				files = append(files, candidate{path: markdown, nested: true})
			}
			continue
		}
		if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(name), ".md") {
			files = append(files, candidate{path: path})
		}
	}
	return files
}

// skillFileIn finds the SKILL.md inside a skill directory.
//
// The comparison folds case because the alternative is a skill that silently
// does not exist: on a case-insensitive filesystem skill.md and SKILL.md are the
// same file, and which name ReadDir reports is not something the author chose.
func skillFileIn(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(entry.Name(), skillFileName) {
			continue
		}
		return filepath.Join(dir, entry.Name()), true
	}
	return "", false
}

// loadFile reads one candidate. A nil skill means "skip", and the notes say why:
// a broken skill must not stop a session from starting, but it must not vanish in
// silence either — the author believes it is installed.
func loadFile(file candidate, scope Scope, limits Limits) (*Skill, []string) {
	content, err := os.ReadFile(file.path)
	if err != nil {
		return nil, []string{problem(file.path, "%v", err)}
	}
	text := stripBOM(string(content))
	values, err := parseFrontmatter(text)
	if err != nil {
		return nil, []string{problem(file.path, "%v", err)}
	}

	name := values["name"]
	if name == "" {
		return nil, []string{problem(file.path, "no name in the frontmatter; a skill is listed and called by its name")}
	}
	if why := checkName(name); why != "" {
		return nil, []string{problem(file.path, "the name %q %s", name, why)}
	}
	description := strings.TrimSpace(values["description"])
	if description == "" {
		return nil, []string{problem(file.path, "no description in the frontmatter; it is the only thing the model chooses a skill by, so a skill without one is never picked")}
	}

	found := &Skill{Name: name, Description: description, Path: file.path, Scope: scope}
	notes := make([]string, 0, 3)
	if runes := []rune(description); len(runes) > maxDescription {
		notes = append(notes, problem(file.path, "the description is %d characters and was cut at %d; what is left still says when to use it", len(runes), maxDescription))
		found.Description = strings.TrimSpace(string(runes[:maxDescription]))
	}
	if file.nested {
		// The specification wants the two to match. Rejecting the mismatch would
		// turn a directory rename into a skill that disappeared, so the name wins
		// and the reader is told.
		if dir := filepath.Base(filepath.Dir(file.path)); dir != name {
			notes = append(notes, problem(file.path, "the name %q does not match its directory %q, which the specification asks for; the name wins", name, dir))
		}
	}
	// Both keys are read only to say they are not read. Silently ignoring them
	// would leave an author believing they had pre-approved or withheld a tool.
	if strings.TrimSpace(values["allowed-tools"]) != "" {
		notes = append(notes, problem(file.path, "allowed-tools is not read: approvals here are asked once per tool per session, so a declaration in a file cannot pre-approve anything"))
	}
	if strings.TrimSpace(values["disallowed-tools"]) != "" {
		notes = append(notes, problem(file.path, "disallowed-tools is not read: the tools it names are still available to this session"))
	}
	if why := limits.exceeded(content, text); why != "" {
		notes = append(notes, problem(file.path, "%s", why))
	}
	return found, notes
}

// exceeded says why the file is longer than the tool that reads it will show, or
// "" when it fits. A truncated instruction is the worst outcome in this feature:
// the model believes it has the whole skill and follows half of it, and nothing
// in the conversation says otherwise.
func (l Limits) exceeded(content []byte, text string) string {
	if l.MaxBytes > 0 && len(content) > l.MaxBytes {
		return fmt.Sprintf("the file is %d bytes and reading stops at %d, so the model would get a truncated skill; move the long part into a file beside it and point at that", len(content), l.MaxBytes)
	}
	if l.MaxLines > 0 {
		lines := strings.Count(text, "\n")
		if !strings.HasSuffix(text, "\n") {
			lines++
		}
		if lines > l.MaxLines {
			return fmt.Sprintf("the file is %d lines and reading stops at %d, so the model would get a truncated skill; move the long part into a file beside it and point at that", lines, l.MaxLines)
		}
	}
	return ""
}

// checkName returns why a name cannot be used, or "".
//
// The rule is the specification's. It is enforced rather than reported because
// the name is what /skill takes: one that cannot be typed is not a skill the
// user can call, and listing it would promise something that does not work.
func checkName(name string) string {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return "must be lowercase letters, digits, and - only"
	}
	if len(name) > maxName {
		return fmt.Sprintf("is %d characters, and a name is at most %d", len(name), maxName)
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return "must not start or end with -"
	}
	if strings.Contains(name, "--") {
		return "must not contain --"
	}
	return ""
}

// Directory renders the block that tells the model which skills exist, or ""
// when there are none.
//
// Only metadata goes in, and the guidance follows the list rather than
// preceding it: a model reading down a long prompt keeps the end of it, and
// "read the file before you start" is the one instruction that makes the list
// usable. An empty list renders nothing at all, because an empty element is a
// sentence about a feature rather than about the task.
func Directory(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	lines := make([]string, 0, len(skills)*5+3)
	lines = append(lines, "<available_skills>")
	for _, s := range skills {
		lines = append(lines,
			"<skill>",
			"<name>"+inline(s.Name)+"</name>",
			"<description>"+inline(s.Description)+"</description>",
			"<location>"+inline(s.Path)+"</location>",
			"</skill>",
		)
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n") + "\n\n" + skillGuidance
}

// skillGuidance is what makes the list worth its space. Without the first
// sentence the model reads a description as if it were the instructions; without
// the second it resolves "references/api.md" against the working directory and
// either reads the wrong file or none.
const skillGuidance = "A skill is instructions for a kind of task, not a tool. When a task matches one of the descriptions above, read that skill's location before you start, and resolve a relative path inside it against the directory the file is in."

// inline puts a value on one line of the block. Whitespace is folded because a
// description that wraps would move the closing tag off the line the reader
// expects, and the angle brackets are escaped so a description containing
// </skill> cannot end the element it sits in.
func inline(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}

// problem formats one warning. The prefix is the shape config and mcp use: a
// reader with four roots needs to know which file is the one to fix.
func problem(path, format string, args ...any) string {
	return "skill: " + path + ": " + fmt.Sprintf(format, args...)
}

// stripBOM removes a UTF-8 byte order mark, which editors on Windows write and
// which would otherwise be the first character the frontmatter reader sees.
func stripBOM(text string) string {
	return strings.TrimPrefix(text, "\ufeff")
}

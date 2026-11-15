package main

// The project's context files: the AGENTS.md a repository keeps at its root,
// read once at startup and appended to the system prompt.
//
// This is the half of /init that makes the file worth writing. The other half is
// the turn that writes it, and on its own it produces a document for somebody
// else — an agent that never reads the file it was asked to write has said
// nothing to itself.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pi-go/internal/config"
)

// contextFileNames are the candidates in one directory, most specific first.
//
// The override exists for the case the plain name cannot express: a generated or
// vendored AGENTS.md that a checkout wants to shadow without editing a file that
// is about to be regenerated. The CLAUDE spellings are here because a repository
// that already carries guidance for another agent should not have to keep a
// second copy of it to be readable by this one.
var contextFileNames = []string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

// contextFile is one file that applies to the work, with the path kept beside the
// text: the model is told which file said it, so a rule that is wrong can be
// traced to the file that is wrong.
type contextFile struct {
	path    string
	content string
}

// projectContext renders the context files that apply to cwd as one block and
// returns the paths it read, or "" when the project has none.
//
// The paths come back with the text because they are the proof that a file
// was read: /reload reports them so a reader who just wrote AGENTS.md sees
// which file the prompt was made of.
//
// The heading is part of the message rather than a comment for the reader of this
// file: without it the model sees a pile of project rules in the same breath as
// its own instructions, and cannot tell which of the two it is allowed to
// question.
func projectContext(cwd string) (string, []string, error) {
	files, err := contextFiles(cwd)
	if err != nil {
		return "", nil, err
	}
	if len(files) == 0 {
		return "", nil, nil
	}
	blocks := make([]string, 0, len(files)+1)
	blocks = append(blocks, "Project-specific instructions and guidelines:")
	paths := make([]string, 0, len(files))
	for _, file := range files {
		blocks = append(blocks, fmt.Sprintf("<project_instructions path=%q>\n%s\n</project_instructions>", file.path, file.content))
		paths = append(paths, file.path)
	}
	return strings.Join(blocks, "\n\n"), paths, nil
}

// contextFiles collects the files that apply to cwd: the user's global one first,
// then one per directory from the outermost ancestor down to cwd.
//
// Walking up is the point of the walk rather than a bonus: a monorepo keeps one
// file at the root, and a package inside it adds its own without restating the
// root's rules. Order follows the same logic — the last thing the model reads is
// the file closest to the code it is about to change, which is the one most
// likely to be specific to it.
//
// Every ancestor is read, not just the repository root. A file in the home
// directory therefore applies to everything under it, which is how it is meant to
// read: a file at that level is a statement about the machine, not about a
// project.
func contextFiles(cwd string) ([]contextFile, error) {
	files := make([]contextFile, 0, 4)
	seen := make(map[string]bool, 4)

	// The global file is optional and quietly so: a fresh install has no
	// ~/.atlas/AGENTS.md, and a warning about a file nobody was told to create
	// would be noise on every run.
	if home, err := config.DefaultHome(); err == nil {
		if file, ok, err := contextFileIn(home); err != nil {
			return nil, err
		} else if ok {
			files = append(files, file)
			seen[file.path] = true
		}
	}

	// Ancestors are collected downwards, so they are prepended: the walk runs
	// from the work outwards, and the rendering has to run from the outside in.
	ancestors := make([]contextFile, 0, 4)
	for dir := cwd; ; {
		file, ok, err := contextFileIn(dir)
		if err != nil {
			return nil, err
		}
		if ok && !seen[file.path] {
			ancestors = append([]contextFile{file}, ancestors...)
			seen[file.path] = true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return append(files, ancestors...), nil
}

// contextFileIn reads the first usable candidate in dir. The bool is "this
// directory has one" rather than an error, because most directories do not.
//
// A file that cannot be read is an error, not a skip: silently dropping it means
// the model answers without instructions the reader believes it was given, and
// the failure surfaces as a wrong answer instead of a path.
func contextFileIn(dir string) (contextFile, bool, error) {
	for _, name := range contextFileNames {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return contextFile{}, false, fmt.Errorf("context file: %w", err)
		}
		// Stat follows symlinks, so a link to a real file counts. Anything else
		// called AGENTS.md is a build artifact or a mount point, not guidance.
		if !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return contextFile{}, false, fmt.Errorf("context file: %w", err)
		}
		text := strings.TrimRight(stripBOM(string(content)), "\n")
		if strings.TrimSpace(text) == "" {
			// An empty file is not a policy, and rendering an empty block would
			// tell the model that a project said something without saying what.
			// The next candidate is still worth reading: an empty AGENTS.md must
			// not hide a CLAUDE.md that has content.
			continue
		}
		return contextFile{path: path, content: text}, true, nil
	}
	return contextFile{}, false, nil
}

// stripBOM removes a UTF-8 byte order mark, which editors on Windows write and
// which would otherwise be the first character the model reads.
func stripBOM(text string) string {
	return strings.TrimPrefix(text, "\ufeff")
}

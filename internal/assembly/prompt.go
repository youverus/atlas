package assembly

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync"

	"pi-go/internal/config"
	"pi-go/internal/skill"
)

// builtinPrompt ships inside the binary. A separate file would be a second
// artifact to install, and the point of a static binary is that it is the whole
// product.
//
//go:embed system.md
var builtinPrompt string

// BasePrompt returns the instructions before the project's own files are
// appended: the built-in ones, or a file when the user pointed at one.
//
// A file replaces the base and nothing else. The project's context files are
// facts about the code, not a second opinion about how to be an agent, so a
// caller who brought their own instructions still gets them.
func BasePrompt(path string) (string, error) {
	if path == "" {
		return builtinPrompt, nil
	}
	expanded, err := config.ExpandHome(path)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(expanded)
	if err != nil {
		return "", fmt.Errorf("read system prompt: %w", err)
	}
	return string(content), nil
}

// Prompts is the prompt question asked of the current directory.
//
// It is a value rather than a string because the answer changes: the command
// writes the file the prompt is made of (/init), and a session that only read
// it at startup would show the user a file it will not obey until it is
// restarted.
type Prompts struct {
	file      string
	noContext bool
	noSkills  bool
}

// NewPrompts is the builder a front end describes: which base file, and the two
// switches that turn off the project files and the skill list.
func NewPrompts(file string, noContext, noSkills bool) Prompts {
	return Prompts{file: file, noContext: noContext, noSkills: noSkills}
}

// PromptBuild is one rendering of the prompt and what went into it.
//
// The counts and the paths are what /reload reports: a reader who just wrote
// a file wants to see that it was read, and a count alone does not say which
// file it was.
type PromptBuild struct {
	// Text is the instructions themselves.
	Text string
	// ContextFiles are the project files that were read, in the order they
	// were applied.
	ContextFiles []string
	// Skills is how many skills the block listed.
	Skills int
	// Notes are the warnings about skills that were skipped or accepted with
	// a caveat.
	Notes []string
}

// build renders the prompt for cwd: the base, then the context files, then the
// skills that are available.
//
// The order is by how specific each block is, and the last one wins a reader's
// attention: the base is how to be an agent, the project files are what this
// code is, and the skill list is what this task could be. A directory with
// nothing to add gets the base byte for byte, which is what keeps -demo and
// every test of the built-in prompt saying what they said before.
//
// The notes are warnings about skills that were skipped or accepted with a
// caveat. They are returned rather than printed because a rebuild happens where
// there is no terminal writer, and a second scan to print them would answer a
// question the first scan already answered.
func (b Prompts) Build(cwd string) (PromptBuild, error) {
	base, err := BasePrompt(b.file)
	if err != nil {
		return PromptBuild{}, err
	}
	var blocks []string
	var contextFiles []string
	if !b.noContext {
		context, files, err := ProjectContext(cwd)
		if err != nil {
			return PromptBuild{}, err
		}
		if context != "" {
			blocks = append(blocks, context)
			contextFiles = files
		}
	}
	var notes []string
	skills := 0
	if !b.noSkills {
		found, skillNotes := AvailableSkills(cwd)
		notes = skillNotes
		skills = len(found)
		if directory := skill.Directory(found); directory != "" {
			blocks = append(blocks, directory)
		}
	}
	text := base
	if len(blocks) > 0 {
		text = strings.TrimRight(base, "\n") + "\n\n" + strings.Join(blocks, "\n\n") + "\n"
	}
	return PromptBuild{Text: text, ContextFiles: contextFiles, Skills: skills, Notes: notes}, nil
}

// note says whether the context files were read, for the one command whose
// output is about them.
//
// The silence of the default case is the point: a session does not announce
// which files it read on every start, and /init is the only place where the
// answer is not obvious from the file that was just written.
func (b Prompts) Note() string {
	if b.noContext {
		return " · not read: -no-context-files is set"
	}
	return " · read into the prompt"
}

// PromptSource is the live prompt: the instructions are read from the files
// behind them every time they are asked for.
//
// It is asked once per turn, so a project file that changed and a skill that
// was installed take effect on the next question without restarting the
// session. It is a struct rather than a closure because the notes belong to
// the last build, and /reload is the command that reports them.
//
// The directory is not a field: the app asks with the one the session belongs
// to, and a session can move to another project's conversation while this
// process runs. A builder bound to the startup directory would keep reading
// the instructions of a project the reader left.
type PromptSource struct {
	builder Prompts

	mu    sync.Mutex
	build PromptBuild
}

// source turns the builder into the prompt the app asks for.
func (b Prompts) Source() *PromptSource {
	return &PromptSource{builder: b}
}

// prompt reads the instructions for cwd. It is the source the app asks for.
func (s *PromptSource) Prompt(cwd string) (string, error) {
	build, err := s.Read(cwd)
	if err != nil {
		return "", err
	}
	return build.Text, nil
}

// read reads the instructions and remembers what the read found.
func (s *PromptSource) Read(cwd string) (PromptBuild, error) {
	build, err := s.builder.Build(cwd)
	if err != nil {
		return PromptBuild{}, err
	}
	s.mu.Lock()
	s.build = build
	s.mu.Unlock()
	return build, nil
}

// last returns what the last read found. It is empty before the first one.
func (s *PromptSource) Last() PromptBuild {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.build
}

// note says whether the context files were read, for the one command whose
// output is about them.
func (s *PromptSource) Note() string {
	return s.builder.Note()
}

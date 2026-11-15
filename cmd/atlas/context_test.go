package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// putFile writes one file, creating the directories above it.
func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// emptyHome keeps the machine's own directories out of the walk and returns the
// atlas home it chose: $ATLAS_HOME/skills is one of the roots a skill is read
// from.
//
// HOME is redirected too, and for a different reason: the shared skill root is
// ~/.agents/skills, which on a machine that runs pi holds real skills. Without
// this, a developer would see a different prompt than CI and neither would be
// wrong.
func emptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	user := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	t.Setenv("HOME", user)
	// os.UserHomeDir reads USERPROFILE on Windows.
	t.Setenv("USERPROFILE", user)
	return home
}

// The walk is the feature: a monorepo keeps one file at the root, a package adds
// its own, and the model has to see the package's rules last because they are
// the ones about the code in front of it.
func TestTheContextFilesRunFromTheRootDownToTheWork(t *testing.T) {
	emptyHome(t)
	root := t.TempDir()
	putFile(t, filepath.Join(root, "AGENTS.md"), "# root rules\n")
	putFile(t, filepath.Join(root, "pkg", "CLAUDE.md"), "# package rules\n")
	putFile(t, filepath.Join(root, "pkg", "deep", "AGENTS.md"), "# deep rules\n")

	context, paths, err := projectContext(filepath.Join(root, "pkg", "deep"))
	if err != nil {
		t.Fatalf("projectContext: %v", err)
	}
	if !strings.HasPrefix(context, "Project-specific instructions and guidelines:") {
		t.Fatalf("context = %q, want the heading first", context)
	}
	positions := make([]int, 3)
	for i, want := range []string{"# root rules", "# package rules", "# deep rules"} {
		positions[i] = strings.Index(context, want)
		if positions[i] < 0 {
			t.Fatalf("context = %q, missing %q", context, want)
		}
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] > positions[i] {
			t.Fatalf("context = %q, want the outermost file first", context)
		}
	}
	// The path is part of the block: a rule that turns out to be wrong has to
	// be traceable to the file that is wrong.
	if !strings.Contains(context, `path="`+filepath.Join(root, "pkg", "CLAUDE.md")+`"`) {
		t.Fatalf("context = %q, want the path beside the text", context)
	}
	// The paths come back with the text because /reload reports them: a
	// reader who just wrote a file wants to see which file the prompt was
	// made of.
	want := []string{
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "pkg", "CLAUDE.md"),
		filepath.Join(root, "pkg", "deep", "AGENTS.md"),
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i, path := range want {
		if paths[i] != path {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}

// One directory, one file: the first candidate that has content is the
// directory's answer, and the rest are not read.
func TestTheMostSpecificNameInADirectoryWins(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"override", map[string]string{"AGENTS.md": "plain", "AGENTS.override.md": "override"}, "override"},
		{"agents before claude", map[string]string{"AGENTS.md": "agents", "CLAUDE.md": "claude"}, "agents"},
		{"claude alone", map[string]string{"CLAUDE.md": "claude"}, "claude"},
		{"upper case agents", map[string]string{"AGENTS.MD": "upper"}, "upper"},
		{"upper case claude", map[string]string{"CLAUDE.MD": "claude-upper"}, "claude-upper"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emptyHome(t)
			dir := t.TempDir()
			for name, content := range tc.files {
				putFile(t, filepath.Join(dir, name), content)
			}
			file, ok, err := contextFileIn(dir)
			if err != nil {
				t.Fatalf("contextFileIn: %v", err)
			}
			if !ok {
				t.Fatalf("no file found among %v", tc.files)
			}
			if file.content != tc.want {
				t.Fatalf("content = %q, want %q", file.content, tc.want)
			}
		})
	}
}

// An empty file is not a policy. It also must not shadow the neighbour: a
// repository that keeps a generated, empty AGENTS.md beside a hand-written
// CLAUDE.md has said what it means in the second one.
func TestAnEmptyFileDoesNotHideTheNextCandidate(t *testing.T) {
	emptyHome(t)
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "AGENTS.md"), "\n\n")
	putFile(t, filepath.Join(dir, "CLAUDE.md"), "claude rules\n")

	file, ok, err := contextFileIn(dir)
	if err != nil {
		t.Fatalf("contextFileIn: %v", err)
	}
	if !ok || file.content != "claude rules" {
		t.Fatalf("file = %+v (found %v), want the CLAUDE.md", file, ok)
	}
}

// A directory called AGENTS.md is a checkout accident or a build artifact. It
// cannot be read, and a failed read here would stop every session under it.
func TestADirectoryNamedAgentsIsNotGuidance(t *testing.T) {
	emptyHome(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "AGENTS.md"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	putFile(t, filepath.Join(dir, "CLAUDE.md"), "claude rules\n")

	file, ok, err := contextFileIn(dir)
	if err != nil {
		t.Fatalf("contextFileIn: %v", err)
	}
	if !ok || file.content != "claude rules" {
		t.Fatalf("file = %+v (found %v), want the CLAUDE.md", file, ok)
	}
}

// A file that exists and cannot be read is an error. Skipping it would answer
// without instructions the reader believes were given, and the mistake would
// surface as a wrong answer instead of a path.
func TestAContextFileThatCannotBeReadIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through the permission bits")
	}
	emptyHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	putFile(t, path, "rules\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	if _, _, err := projectContext(dir); err == nil {
		t.Fatal("want an error for a file that cannot be read")
	}
}

// The global file is about the machine, so it comes before anything a project
// says: a project may add to it, and it may not be silently outvoted by a file
// nobody wrote on purpose.
func TestTheGlobalFileComesBeforeTheProjectOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	putFile(t, filepath.Join(home, "AGENTS.md"), "# machine rules\n")

	project := t.TempDir()
	putFile(t, filepath.Join(project, "AGENTS.md"), "# project rules\n")

	context, paths, err := projectContext(project)
	if err != nil {
		t.Fatalf("projectContext: %v", err)
	}
	machine, projectAt := strings.Index(context, "# machine rules"), strings.Index(context, "# project rules")
	if machine < 0 || projectAt < 0 {
		t.Fatalf("context = %q, want both files", context)
	}
	if machine > projectAt {
		t.Fatalf("context = %q, want the global file first", context)
	}
	if len(paths) != 2 || paths[0] != filepath.Join(home, "AGENTS.md") {
		t.Fatalf("paths = %v, want the global file first", paths)
	}
}

// A project with no files gets the built-in prompt byte for byte. This is what
// keeps every offline mode and every existing test saying what it said before.
func TestAProjectWithoutContextFilesGetsThePlainPrompt(t *testing.T) {
	emptyHome(t)
	build, err := promptBuilder{}.build(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Text != builtinPrompt {
		t.Fatalf("prompt differs from the built-in one:\n%s", build.Text)
	}
}

// The base keeps its last line and the context follows it, separated by one
// blank line: the two are different voices, and running them together makes the
// built-in instructions end mid-sentence.
func TestTheContextFollowsTheBasePrompt(t *testing.T) {
	emptyHome(t)
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "AGENTS.md"), "# project rules\n")

	build, err := promptBuilder{}.build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasPrefix(build.Text, strings.TrimRight(builtinPrompt, "\n")+"\n\n") {
		t.Fatalf("prompt = %q, want the built-in prompt first", build.Text)
	}
	want := "<project_instructions path=\"" + filepath.Join(dir, "AGENTS.md") + "\">\n# project rules\n</project_instructions>\n"
	if !strings.HasSuffix(build.Text, want) {
		t.Fatalf("prompt = %q, want it to end with %q", build.Text, want)
	}
}

// -system-prompt-file replaces the instructions, not the facts about the code:
// the file is a second voice about how to be an agent, and AGENTS.md is still
// where the project says what it is.
func TestACustomPromptStillGetsTheContext(t *testing.T) {
	emptyHome(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "base.md")
	putFile(t, base, "my own prompt\n")
	putFile(t, filepath.Join(dir, "AGENTS.md"), "# project rules\n")

	build, err := promptBuilder{file: base}.build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasPrefix(build.Text, "my own prompt\n\nProject-specific instructions") {
		t.Fatalf("prompt = %q, want the file then the context", build.Text)
	}
}

// The flag is the way back to exactly the built-in prompt, which is what a
// caller comparing models needs.
func TestNoContextFilesLeavesThePromptAlone(t *testing.T) {
	emptyHome(t)
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "AGENTS.md"), "# project rules\n")

	build, err := promptBuilder{noContext: true}.build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Text != builtinPrompt {
		t.Fatalf("prompt = %q, want the built-in prompt unchanged", build.Text)
	}
}

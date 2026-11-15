package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/tool"
)

// These tests run a real git in a real temporary repository. The tool's contract
// is about what git does with an argv, so a fake would only test the fake.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("these tests need git on PATH: %v", err)
	}
}

// repoEnv isolates the repository from the developer's own configuration: a
// global init.defaultBranch, autocrlf or hooks would otherwise change the output
// these tests assert on.
func repoEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = repoEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// initRepo builds a repository with one committed file, so the tools have a
// history to read.
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", "notes.txt")
	gitRun(t, dir, "commit", "-q", "-m", "the first commit")
	return dir
}

// call runs one tool from the set and returns its model-facing text.
func call(t *testing.T, tools []tool.Tool, name string, args ...string) tool.Result {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Name() != name {
			continue
		}
		encoded, err := json.Marshal(argsRequest{Args: args})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		result, err := candidate.Execute(context.Background(),
			tool.Call{ID: "call-1", Name: name, Arguments: encoded}, func(tool.Partial) {})
		if err != nil {
			t.Fatalf("%s %q: %v", name, args, err)
		}
		return result
	}
	t.Fatalf("no tool named %s", name)
	return tool.Result{}
}

func resultText(t *testing.T, result tool.Result) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(result.Content))
	}
	return result.Content[0].Text
}

func TestStatusReportsTheWorkingTree(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "draft.md"), []byte("new\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The mode of --untracked-files is written with an equals sign, which is the
	// only spelling git accepts; passing it as a separate word would make git read
	// "normal" as a pathspec.
	result := call(t, Tools(Options{Cwd: dir}), "git_status", "--short", "--untracked-files=normal")
	text := resultText(t, result)
	if result.IsError {
		t.Fatalf("status failed: %s", text)
	}
	for _, want := range []string{" M notes.txt", "?? draft.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status = %q, want it to contain %q", text, want)
		}
	}
}

func TestDiffShowsTheChange(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	text := resultText(t, call(t, Tools(Options{Cwd: dir}), "git_diff", "--stat"))
	if !strings.Contains(text, "notes.txt") || !strings.Contains(text, "1 file changed") {
		t.Fatalf("diff --stat = %q, want it to name the file and the count", text)
	}
}

func TestLogShowsTheSubject(t *testing.T) {
	dir := initRepo(t)

	text := resultText(t, call(t, Tools(Options{Cwd: dir}), "git_log", "-n", "1", "--oneline"))
	if !strings.Contains(text, "the first commit") {
		t.Fatalf("log = %q, want the subject", text)
	}
}

func TestShowReadsOneCommit(t *testing.T) {
	dir := initRepo(t)

	text := resultText(t, call(t, Tools(Options{Cwd: dir}), "git_show", "-s", "--format=%s"))
	if strings.TrimSpace(text) != "the first commit" {
		t.Fatalf("show = %q, want the subject alone", text)
	}
}

// A non-zero exit is the model's business: git's own message says what was wrong,
// and the status line says the call failed. Both belong in the result, and the
// result must be marked an error so the loop treats it as one.
func TestAFailedCommandIsAnErrorResult(t *testing.T) {
	dir := initRepo(t)

	result := call(t, Tools(Options{Cwd: dir}), "git_show", "nosuchrevision")
	text := resultText(t, result)
	if !result.IsError {
		t.Fatalf("show of a missing revision reported success: %q", text)
	}
	for _, want := range []string{"nosuchrevision", "exited with code"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result = %q, want it to contain %q", text, want)
		}
	}
}

// The truncation notice has to name a way to ask for less, and must not offer a
// file: nothing was spilled, so naming a path would be a lie the model acts on.
func TestTruncationSaysHowToNarrowIt(t *testing.T) {
	dir := initRepo(t)
	// Short lines, many of them: this has to cross the line limit before the byte
	// limit, or the notice under test would be the byte one.
	var builder strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&builder, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", "big.txt")

	text := resultText(t, call(t, Tools(Options{Cwd: dir}), "git_diff", "--cached"))
	if !strings.Contains(text, "Showing the first 2000 of ") || !strings.Contains(text, " lines.") {
		t.Fatalf("result = %q, want the notice to count the lines shown", text)
	}
	if !strings.Contains(text, `Narrow it with a path after "--"`) {
		t.Fatalf("result = %q, want a way to narrow it", text)
	}
	if strings.Contains(text, "the full output is in") {
		t.Fatalf("result = %q, but nothing was spilled to a file", text)
	}
}

// A refused argument is an error from Execute, not a result the model could read
// as success, and git must never be started with it.
func TestARefusedOptionNeverReachesGit(t *testing.T) {
	dir := initRepo(t)
	tools := Tools(Options{Cwd: dir})
	for _, candidate := range tools {
		if candidate.Name() != "git_diff" {
			continue
		}
		encoded, err := json.Marshal(argsRequest{Args: []string{"--textconv"}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		result, err := candidate.Execute(context.Background(),
			tool.Call{ID: "call-1", Name: "git_diff", Arguments: encoded}, func(tool.Partial) {})
		if err == nil {
			t.Fatalf("--textconv ran: %+v", result)
		}
		if !strings.Contains(err.Error(), "runs the diff driver") {
			t.Fatalf("error = %q, want the reason", err)
		}
		return
	}
	t.Fatal("git_diff is not registered")
}

// The tools promise not to write. status is the one command that would, by
// refreshing the index, so the promise is asserted on the bytes of the index
// rather than trusted.
func TestStatusLeavesTheIndexAlone(t *testing.T) {
	dir := initRepo(t)
	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	// A stat-dirty file is what makes status want to rewrite the index.
	old := time.Unix(1_000_000, 0)
	if err := os.Chtimes(filepath.Join(dir, "notes.txt"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	call(t, Tools(Options{Cwd: dir}), "git_status")

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("git_status rewrote the index; GIT_OPTIONAL_LOCKS=0 is not reaching git")
	}
}

// A machine without git gets no tools at all: a tool that always fails costs a
// turn each time the model tries it, while an absent tool costs nothing.
func TestToolsWithoutGitAreNotRegistered(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if tools := Tools(Options{Cwd: t.TempDir()}); tools != nil {
		names := make([]string, 0, len(tools))
		for _, candidate := range tools {
			names = append(names, candidate.Name())
		}
		t.Fatalf("registered %v without git on PATH", names)
	}
}

func TestToolsAreNamedAndDeclared(t *testing.T) {
	tools := Tools(Options{Cwd: t.TempDir(), Path: "/usr/bin/git"})
	want := []string{"git_status", "git_diff", "git_log", "git_show"}
	if len(tools) != len(want) {
		t.Fatalf("tools = %d, want %d", len(tools), len(want))
	}
	for i, candidate := range tools {
		if candidate.Name() != want[i] {
			t.Fatalf("tool %d = %q, want %q", i, candidate.Name(), want[i])
		}
		if candidate.Label() != want[i] {
			t.Fatalf("%s label = %q", want[i], candidate.Label())
		}
		if candidate.Description() == "" {
			t.Fatalf("%s has no description", want[i])
		}
		if !strings.Contains(string(candidate.Parameters()), `"args"`) {
			t.Fatalf("%s parameters do not declare args", want[i])
		}
	}
}

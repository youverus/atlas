package main

// The footer's first field says where the session runs, and the branch is the
// second half of that question. It comes from the repository's own files rather
// than from git, so these tests build repositories out of the two things that
// matter: a .git entry and the HEAD inside it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/wire"
)

// repository writes a .git directory with HEAD saying what head says.
func repository(t *testing.T, head string) string {
	t.Helper()
	dir := t.TempDir()
	writeHead(t, filepath.Join(dir, ".git"), head)
	return dir
}

// writeHead writes gitDir/HEAD, creating the directory.
func writeHead(t *testing.T, gitDir, head string) {
	t.Helper()
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestTheBranchIsReadFromTheRepository(t *testing.T) {
	dir := repository(t, "ref: refs/heads/main\n")
	if got := (&gitRef{}).branch(dir); got != "main" {
		t.Fatalf("branch is %q, want %q", got, "main")
	}
}

// TestABranchWithASlashKeepsIt: refs/heads is the only prefix that goes.
func TestABranchWithASlashKeepsIt(t *testing.T) {
	dir := repository(t, "ref: refs/heads/personal/dfbuddy\n")
	if got := (&gitRef{}).branch(dir); got != "personal/dfbuddy" {
		t.Fatalf("branch is %q, want %q", got, "personal/dfbuddy")
	}
}

// TestTheRepositoryIsFoundFromASubdirectory: a session started inside a package
// is still in the repository that contains it.
func TestTheRepositoryIsFoundFromASubdirectory(t *testing.T) {
	dir := repository(t, "ref: refs/heads/main\n")
	deep := filepath.Join(dir, "go", "internal", "tui")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if got := (&gitRef{}).branch(deep); got != "main" {
		t.Fatalf("branch from a subdirectory is %q, want %q", got, "main")
	}
}

// TestADetachedHeadReportsItsShortCommit: the reader is somewhere in the
// history, and a footer that fell silent there would look like a lost
// repository.
func TestADetachedHeadReportsItsShortCommit(t *testing.T) {
	dir := repository(t, "7a5548bf4d1e2c3b4a5968778695a4b3c2d1e0f9\n")
	if got := (&gitRef{}).branch(dir); got != "7a5548b" {
		t.Fatalf("branch is %q, want the short commit %q", got, "7a5548b")
	}
}

// TestAWorktreeIsFollowed: a linked worktree has .git as a file holding
// "gitdir: <path>", and the HEAD that says the branch is at the end of it.
func TestAWorktreeIsFollowed(t *testing.T) {
	parent := t.TempDir()
	meta := filepath.Join(parent, "main", ".git", "worktrees", "feature")
	writeHead(t, meta, "ref: refs/heads/feature\n")

	worktree := filepath.Join(parent, "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+meta+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := (&gitRef{}).branch(worktree); got != "feature" {
		t.Fatalf("branch in a worktree is %q, want %q", got, "feature")
	}
}

// TestARelativeGitdirIsResolvedAgainstTheWorktree: the file may name the
// metadata relative to itself, which is what git writes for a submodule.
func TestARelativeGitdirIsResolvedAgainstTheWorktree(t *testing.T) {
	parent := t.TempDir()
	worktree := filepath.Join(parent, "sub")
	writeHead(t, filepath.Join(worktree, ".repo", "sub"), "ref: refs/heads/submodule\n")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: .repo/sub\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := (&gitRef{}).branch(worktree); got != "submodule" {
		t.Fatalf("branch is %q, want %q", got, "submodule")
	}
}

// TestAWorktreeWithoutAHeadReportsNothing: a .git file that names nowhere, or a
// directory in the middle of being created, has no branch to report.
func TestAWorktreeWithoutAHeadReportsNothing(t *testing.T) {
	parent := t.TempDir()
	cases := map[string]string{
		"nowhere":   "gitdir: " + filepath.Join(parent, "absent") + "\n",
		"no gitdir": "something else\n",
		"empty":     "\n",
	}
	for name, content := range cases {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if got := (&gitRef{}).branch(dir); got != "" {
			t.Fatalf("%s reported the branch %q", name, got)
		}
	}
}

// TestAGitDirectoryWithoutAHeadReportsNothing: a repository in the middle of
// being created has nothing to say about a branch.
func TestAGitDirectoryWithoutAHeadReportsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if got := (&gitRef{}).branch(dir); got != "" {
		t.Fatalf("branch is %q, want nothing", got)
	}
}

func TestOutsideARepositoryThereIsNoBranch(t *testing.T) {
	if got := (&gitRef{}).branch(t.TempDir()); got != "" {
		t.Fatalf("branch outside a repository is %q, want nothing", got)
	}
}

// TestTheBranchIsReReadWhenItChanges: git rewrites HEAD in place on a checkout,
// and the footer is a line about the code as it is now.
func TestTheBranchIsReReadWhenItChanges(t *testing.T) {
	dir := repository(t, "ref: refs/heads/main\n")
	ref := &gitRef{}
	if got := ref.branch(dir); got != "main" {
		t.Fatalf("branch is %q, want %q", got, "main")
	}

	head := filepath.Join(dir, ".git", "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/a-longer-branch\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(head, later, later); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if got := ref.branch(dir); got != "a-longer-branch" {
		t.Fatalf("branch after a checkout is %q, want %q", got, "a-longer-branch")
	}
}

// TestARepositoryThatWentAwayIsForgotten: a removed .git must stop answering,
// and a repository that comes back must be found again rather than remembered
// as absent.
func TestARepositoryThatWentAwayIsForgotten(t *testing.T) {
	dir := repository(t, "ref: refs/heads/main\n")
	ref := &gitRef{}
	if got := ref.branch(dir); got != "main" {
		t.Fatalf("branch is %q, want %q", got, "main")
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if got := ref.branch(dir); got != "" {
		t.Fatalf("a removed repository still reports %q", got)
	}

	writeHead(t, filepath.Join(dir, ".git"), "ref: refs/heads/again\n")
	if got := ref.branch(dir); got != "again" {
		t.Fatalf("a repository that came back reports %q, want %q", got, "again")
	}
}

func TestParseHead(t *testing.T) {
	cases := map[string]string{
		"ref: refs/heads/main\n":                   "main",
		"  ref: refs/heads/x  \n":                  "x",
		"7a5548bf4d1e2c3b4a5968778695a4b3c2d1e0f9": "7a5548b",
		"7a5548":            "",
		"\n":                "",
		"ref: refs/tags/v1": "refs/tags/v1",
	}
	for head, want := range cases {
		if got := parseHead(head); got != want {
			t.Fatalf("parseHead(%q) = %q, want %q", head, got, want)
		}
	}
}

// TestTheBranchRidesInTheDirectoryField: the two answer one question, so the
// branch is glued to the directory rather than given a separator of its own. The
// bar reports the name as read; where it rides is dirFrames' decision.
// TestTheFooterNamesTheBranchItRunsOn joins the three pieces once — the bar reads
// the name, statusInfo paints it, line composes the frame — so a change to any of
// them shows up here and not only in the unit beside it. The branch has a slash in
// it on purpose: that is the shape the footer used to cut in half.
func TestTheFooterNamesTheBranchItRunsOn(t *testing.T) {
	dir := repository(t, "ref: refs/heads/personal/dfbuddy\n")
	application, err := app.New(app.Config{
		Model:    wire.Model{ID: "m1", Provider: "test"},
		Provider: cancelled{},
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	line := newStatusBar(application, nil, dir, "fs+bash").line(200)
	if !strings.Contains(line, " (personal/dfbuddy) |") {
		t.Fatalf("the footer does not name the branch whole: %q", line)
	}
}

func TestTheBranchRidesInTheDirectoryField(t *testing.T) {
	dir := repository(t, "ref: refs/heads/main\n")
	bar := &statusBar{dir: "proj", cwd: dir, git: &gitRef{}}
	if got := bar.branch(); got != "main" {
		t.Fatalf("the field says %q", got)
	}

	plain := &statusBar{dir: "proj", cwd: t.TempDir(), git: &gitRef{}}
	if got := plain.branch(); got != "" {
		t.Fatalf("outside a repository the field says %q", got)
	}
}

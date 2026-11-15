package main

// The footer says where the session runs. The branch belongs beside that,
// because "which code is this" is one question and a reader who is looking at a
// directory name is usually asking it.
//
// It is read from the repository rather than from git. The footer is asked for a
// line on every repaint, and a process per frame is not a measurement anyone
// would keep in a status bar; what the branch name costs instead is one small
// file, and the revalidation below is one stat of it.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gitRef finds the branch a directory is on and re-reads it when the file that
// says so changes.
//
// The `.git` entry is a directory in an ordinary clone and a file in a worktree
// or a submodule, where it holds "gitdir: <path>" pointing somewhere else. Both
// are followed here: somebody working in a worktree is not an unusual reader, and
// a footer that went blank for them would be worse than one that never had the
// field.
type gitRef struct {
	mu sync.Mutex
	// head is the file that holds the branch, and "" once the search found
	// none. found records that the search has run: walking the parents of a
	// directory that is not in a repository on every frame would be sixty
	// directory scans a second to keep answering "no".
	head  string
	found bool
	// stamp is the file's modification time and size as it was when it was last
	// read. Git rewrites HEAD in place when the branch changes, so the pair is
	// what tells a remembered name from a current one without reading the file
	// on every frame.
	stamp time.Time
	size  int64
	// name is the branch as last read, and what the method below reports while
	// the file is unchanged. It is not called branch because that is the method.
	name string
}

// branch is the branch name, or the short commit when the repository is not on a
// branch, or "" outside a repository.
//
// A detached HEAD is reported as its short id rather than as nothing: the reader
// is somewhere in the history, and a footer that fell silent would look like a
// repository that had gone away.
func (g *gitRef) branch(cwd string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.found {
		g.head = findHead(cwd)
		g.found = true
	}
	if g.head == "" {
		return ""
	}
	info, err := os.Stat(g.head)
	if err != nil {
		// The repository was moved or removed under the process. Forgetting the
		// path lets the next frame look again, rather than reporting a branch
		// from a file that is no longer there.
		g.head, g.found, g.name = "", false, ""
		return ""
	}
	if info.ModTime().Equal(g.stamp) && info.Size() == g.size {
		return g.name
	}
	content, err := os.ReadFile(g.head)
	if err != nil {
		return g.name
	}
	g.stamp, g.size = info.ModTime(), info.Size()
	g.name = parseHead(string(content))
	return g.name
}

// findHead walks up from dir to the nearest repository and returns the file that
// holds the branch it is on.
//
// Upward, because git does: a session started in a subdirectory is in the
// repository that contains it, and a footer that said nothing there would be
// wrong about the one fact it was asked for.
func findHead(dir string) string {
	for {
		entry := filepath.Join(dir, ".git")
		if info, err := os.Stat(entry); err == nil {
			if info.IsDir() {
				return existingFile(filepath.Join(entry, "HEAD"))
			}
			return headOfWorktree(entry, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// headOfWorktree follows a ".git" that is a file. The path it names is either
// absolute or relative to the directory the file is in, which is what the
// relative form means in a worktree's own metadata.
func headOfWorktree(entry, dir string) string {
	content, err := os.ReadFile(entry)
	if err != nil {
		return ""
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:")
	if !ok {
		return ""
	}
	target := strings.TrimSpace(rest)
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return existingFile(filepath.Join(target, "HEAD"))
}

// existingFile is path when something is there, and "" when it is not. A `.git`
// without a HEAD is a repository in the middle of being created or one that was
// damaged; either way there is no branch to report.
func existingFile(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// parseHead reads what HEAD says: a reference to a branch, or the commit itself
// when the repository is detached.
//
// The short id is seven characters, which is the length everything else prints
// and enough to tell two commits apart in a field that has at most a handful of
// cells.
func parseHead(content string) string {
	content = strings.TrimSpace(content)
	if name, ok := strings.CutPrefix(content, "ref: "); ok {
		return strings.TrimPrefix(strings.TrimSpace(name), "refs/heads/")
	}
	if len(content) >= 7 {
		return content[:7]
	}
	return ""
}

// branch is the branch the session runs on, which the footer shows beside the
// directory. It comes from the repository at cwd rather than from dir, because
// dir has already been shortened by the time the footer asks.
//
// It returns the name alone: how the name is drawn next to a path is the
// footer's business, and it depends on the width the footer has to work with.
// That drawing lives in statusInfo.dirFrames.
func (s *statusBar) branch() string {
	return s.git.branch(s.cwd)
}

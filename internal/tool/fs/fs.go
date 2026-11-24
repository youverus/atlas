// Package fs implements the file-facing tools: read, write, edit, ls, find and
// grep.
//
// pi delegates find and grep to the fd and ripgrep binaries. A single static Go
// binary cannot assume either is installed, so both are implemented in process
// here. The externally visible contract is identical: the same parameters, the
// same output wording, the same gitignore-aware traversal, and the same
// truncation notices.
//
// Two intentional gaps are documented in go/docs/architecture.md: images are
// attached without being resized, and gitignore support omits global excludes
// files and backslash escapes.
package fs

import (
	"cmp"
	"os"
	"slices"

	"pi-go/internal/tool"
)

// Options configures the tool set.
type Options struct {
	// Cwd is the directory relative paths resolve against in ordinary mode.
	Cwd string
	// Root enables read-only tools rooted at this directory. Relative paths
	// resolve against Root. Open it with an absolute path to also accept absolute
	// tool paths under that name. The caller owns Root and keeps it open while
	// the tools are in use.
	Root *os.Root
}

// Tools returns the file tools in the order they are registered.
func Tools(options Options) []tool.Tool {
	set := &shared{cwd: options.Cwd, root: options.Root}
	if set.root != nil {
		return []tool.Tool{readTool{set: set}, lsTool{set: set}, findTool{set: set}, grepTool{set: set}}
	}
	set.queue = newMutationQueue()
	return []tool.Tool{
		readTool{set: set},
		writeTool{set: set},
		editTool{set: set},
		lsTool{set: set},
		findTool{set: set},
		grepTool{set: set},
	}
}

type shared struct {
	cwd   string
	root  *os.Root
	queue *mutationQueue
}

// resolve turns an ordinary tool argument into a path relative to cwd.
func (s *shared) resolve(path string) string {
	return resolveToCwd(path, s.cwd)
}

func (s *shared) stat(path string) (os.FileInfo, error) {
	if s.root != nil {
		return s.root.Stat(path)
	}
	return os.Stat(path)
}

func (s *shared) readFile(path string) ([]byte, error) {
	if s.root != nil {
		return s.root.ReadFile(path)
	}
	return os.ReadFile(path)
}

func (s *shared) readDir(path string) ([]os.DirEntry, error) {
	if s.root == nil {
		return os.ReadDir(path)
	}
	dir, err := s.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	// Match the name order of os.ReadDir so search and truncation are stable.
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return cmp.Compare(a.Name(), b.Name()) })
	return entries, err
}

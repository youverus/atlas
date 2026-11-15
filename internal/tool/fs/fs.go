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

import "pi-go/internal/tool"

// Options configures the tool set.
type Options struct {
	// Cwd is the directory relative paths resolve against. It is captured once
	// at construction so a tool call cannot observe a changing working
	// directory halfway through.
	Cwd string
}

// Tools returns the file tools in the order they are registered.
func Tools(options Options) []tool.Tool {
	set := &shared{cwd: options.Cwd, queue: newMutationQueue()}
	return []tool.Tool{
		readTool{set: set},
		writeTool{set: set},
		editTool{set: set},
		lsTool{set: set},
		findTool{set: set},
		grepTool{set: set},
	}
}

// shared is the state every file tool needs: where relative paths resolve, and
// the queue that keeps concurrent mutations of one file from interleaving.
type shared struct {
	cwd   string
	queue *mutationQueue
}

// resolve turns a tool argument into an absolute path.
func (s *shared) resolve(path string) string {
	return resolveToCwd(path, s.cwd)
}

package fs

import (
	"context"
	"path/filepath"
	"strings"
)

// walker recurses a directory tree, pruning anything gitignore excludes. A hand
// written recursion is used instead of filepath.WalkDir because ignore rules
// live in a stack that has to be pushed on entry and popped on exit, and because
// a result limit must stop the walk immediately rather than drain the tree.
type walker struct {
	ctx     context.Context
	set     *shared
	ignores *ignoreStack
	// visit is called for every entry that survived pruning. Returning false
	// stops the walk.
	visit func(entry walkEntry) (bool, error)
	// includeDirs controls whether directories are reported as matches.
	includeDirs bool
}

type walkEntry struct {
	// Rel is the path relative to the walk root, with forward slashes.
	Rel string
	// Path is the path used by the filesystem operations.
	Path string
	// IsDir reports whether the entry is a directory.
	IsDir bool
}

// walkTree calls w.visit for each entry under root. Symbolic links are not
// followed, matching fd and ripgrep defaults.
func (w *walker) walkTree(root string) error {
	return w.walkDir(root, "")
}

func (w *walker) walkDir(dir, prefix string) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	entries, err := w.set.readDir(dir)
	if err != nil {
		// An unreadable directory is not fatal: a permission error deep in a
		// tree should not fail the whole search.
		if prefix == "" {
			return err
		}
		return nil
	}

	pushed := w.ignores.push(dir, prefix, w.set.readFile)
	defer w.ignores.pop(pushed)

	for _, entry := range entries {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		// .git holds object files that would swamp any search and never carry
		// useful matches.
		if name == ".git" {
			continue
		}
		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}
		full := joinAccessPath(dir, name)
		isDir := entry.IsDir()
		if w.ignores.ignored(rel, isDir) {
			continue
		}

		if isDir {
			if w.includeDirs {
				more, err := w.visit(walkEntry{Rel: rel, Path: full, IsDir: true})
				if err != nil {
					return err
				}
				if !more {
					return nil
				}
			}
			if err := w.walkDir(full, rel); err != nil {
				return err
			}
			continue
		}

		if !entry.Type().IsRegular() {
			continue
		}
		more, err := w.visit(walkEntry{Rel: rel, Path: full})
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

// toSlash normalizes a relative path for output. Tool output is always
// slash-separated so a transcript looks the same on every platform.
func toSlash(rel string) string {
	if filepath.Separator == '/' {
		return rel
	}
	return strings.ReplaceAll(rel, string(filepath.Separator), "/")
}

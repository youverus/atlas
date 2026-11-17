package assembly

import (
	"fmt"
	"os"
	"path/filepath"

	"pi-go/internal/config"
	"pi-go/internal/session"
)

// OpenSession opens the session a set of options selects, or starts one.
//
// The three selectors are the three the command line has: one named file, "the
// newest for this directory", or nothing at all. Resolving a -resume prefix
// stays with the caller: that needs the session list read through a menu, and a
// window with no menu would either duplicate the search or get a different
// answer from the terminal beside it.
//
// keepMemory makes a fresh session in memory. It exists for the offline demo,
// where a smoke test must leave nothing behind, and for a front end that wants a
// conversation without deciding where it lives.
func OpenSession(o Options, dir, cwd string, keepMemory bool) (*session.Manager, string, error) {
	switch {
	case o.SessionPath != "":
		path, err := ResolveDir(o.SessionPath)
		if err != nil {
			return nil, "", err
		}
		return openRecorded(path, dir, cwd, o.Workdir != "")
	case o.ContinueLast:
		manager, err := session.ContinueRecent(cwd, dir)
		return manager, "", err
	}
	// A session flag asks for a file, and then the demo must write one: without
	// a flag the demo stays in memory so a smoke test leaves nothing behind.
	if keepMemory && o.SessionDir == "" {
		manager, err := session.InMemory(cwd, session.Options{})
		return manager, "", err
	}
	manager, err := session.Create(cwd, dir, session.Options{})
	return manager, "", err
}

// openRecorded opens a session file named by -session or -resume. The cwd
// override stays empty unless the recorded directory is gone: an opened session
// keeps the directory it was recorded in, and rewriting it would make the header
// disagree with the messages.
//
// A deleted directory is a startup failure, not a surprise inside the first
// tool call. -cwd is the explicit way to say "run it here instead"; without it
// the run stops while no tool has touched anything yet.
func openRecorded(path, dir, cwd string, override bool) (*session.Manager, string, error) {
	manager, err := session.Open(path, dir, "")
	if err != nil {
		return nil, "", err
	}
	missing, gone := manager.MissingCwd()
	if !gone {
		return manager, "", nil
	}
	if !override {
		return nil, "", fmt.Errorf("session %s was recorded in %s, which no longer exists; cd %s to run it there, or pass -cwd %s to run it in the current directory %s", path, missing, missing, cwd, cwd)
	}
	reopened, err := session.Open(path, dir, cwd)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("session %s was recorded in %s, which no longer exists; running it in %s (-cwd)", path, missing, cwd)
	return reopened, note, nil
}

// SessionDir resolves where sessions live: the flag, then the config, then the
// pi-style default under the atlas home, which is
// <home>/sessions/<encoded cwd>.
func SessionDir(o Options, cfg *config.Config, cwd string) (string, error) {
	raw := o.SessionDir
	if raw == "" && cfg != nil {
		raw = cfg.SessionDir
	}
	if raw == "" {
		home, err := config.DefaultHome()
		if err != nil {
			return "", err
		}
		return session.DefaultDir(cwd, home), nil
	}
	return ResolveDir(raw)
}

// ResolveDir expands "~" and makes a path absolute. An empty path means the
// process working directory.
func ResolveDir(path string) (string, error) {
	expanded, err := config.ExpandHome(path)
	if err != nil {
		return "", err
	}
	if expanded == "" {
		return os.Getwd()
	}
	return filepath.Abs(expanded)
}

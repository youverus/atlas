package webhost

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"pi-go/internal/httpapi"
	"pi-go/internal/project"
	"pi-go/internal/session"
)

// Projects draws the rail: the folder conversations start in, then every folder
// the reader added, each with its own conversations.
//
// A group's conversations are the session files whose header records that
// directory, so a project shows what a terminal already wrote there — adding a
// folder is the whole migration. The default group is always drawn and cannot be
// removed: it is where a conversation starts when the reader has picked nothing,
// and a rail with no groups at all would leave no way to start one.
//
// An archived request drops the groups with nothing in the archive rather than
// drawing an empty header for each: the archive is a recovery list, and a column
// of headers over no rows is what it looks like when it is empty.
func (h *Host) Projects(archived bool) (httpapi.ProjectList, error) {
	groups := h.groups()
	list := httpapi.ProjectList{Groups: make([]httpapi.ProjectGroup, 0, len(groups))}
	for _, path := range groups {
		infos, err := h.sessionsIn(path, archived)
		if err != nil {
			return httpapi.ProjectList{}, err
		}
		if archived && len(infos) == 0 {
			continue
		}
		group := httpapi.ProjectGroup{
			Path:     path,
			Name:     filepath.Base(path),
			Default:  sameDir(path, h.defaultDir()),
			Missing:  missing(path),
			Sessions: infos,
		}
		list.Groups = append(list.Groups, group)
	}
	return list, nil
}

// AddProject remembers a folder.
func (h *Host) AddProject(path string) error {
	_, err := h.projects.Add(path)
	return err
}

// RemoveProject forgets a folder. Its conversations stay on disk, and they come
// back the moment the folder is added again.
func (h *Host) RemoveProject(path string) error {
	return h.projects.Remove(path)
}

// NewSessionIn starts a conversation in a folder the reader can see.
//
// The list is what makes a directory reachable from the window, so a folder
// outside it is refused: the page must not be able to start writing session
// files anywhere it likes.
func (h *Host) NewSessionIn(cwd string) error {
	if !h.known(cwd) {
		return fmt.Errorf("webhost: %s is not the default folder or a project", cwd)
	}
	return h.kernel.App.NewSessionIn(cwd)
}

// groups is the drawn order: the default folder first, then the added projects
// in the order they were added.
func (h *Host) groups() []string {
	return mergeGroups(h.defaultDir(), h.projects.List())
}

// mergeGroups is the drawn order, without a kernel: the default folder first,
// then the added projects in the order they were added. A project that is the
// default folder is that one group, not two — the same shortcut listed twice
// would draw the same conversations under two headers.
func mergeGroups(defaultDir string, stored []project.Project) []string {
	drawn := []string{defaultDir}
	for _, entry := range stored {
		duplicate := false
		for _, path := range drawn {
			if sameDir(path, entry.Path) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			drawn = append(drawn, entry.Path)
		}
	}
	return drawn
}

// known reports whether a directory is one the rail draws.
func (h *Host) known(cwd string) bool {
	for _, path := range h.groups() {
		if sameDir(path, cwd) {
			return true
		}
	}
	return false
}

// defaultDir is where a conversation starts when nothing else is chosen: the
// directory this kernel was assembled for.
func (h *Host) defaultDir() string { return h.root() }

// sessionsIn lists one directory's conversations.
//
// The bucket is asked for both the path as written and the same directory with
// its symlinks resolved: a picker hands back the resolved form ("/private/tmp"
// for "/tmp") while the header records what the process was told, and the bucket
// name is derived from the string. Both answers are then filtered by directory
// identity, so a bucket two projects happen to share contributes only its own.
func (h *Host) sessionsIn(cwd string, archived bool) ([]httpapi.SessionInfo, error) {
	current := h.kernel.App.Session().File()
	list := session.List
	if archived {
		list = session.ListArchived
	}
	seen := map[string]bool{}
	out := []httpapi.SessionInfo{}
	for _, candidate := range spellings(cwd) {
		dir, err := h.kernel.App.SessionDir(candidate)
		if err != nil {
			return nil, err
		}
		infos, err := list(dir, "")
		if err != nil {
			// One unreadable bucket must not hide the others, the same rule
			// the session list itself follows.
			continue
		}
		for _, info := range infos {
			if seen[info.Path] || !sameDir(info.Cwd, cwd) {
				continue
			}
			seen[info.Path] = true
			out = append(out, h.sessionInfo(info, archived, current))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Modified > out[j].Modified
	})
	return out, nil
}

// sessionInfo translates one row.
func (h *Host) sessionInfo(info session.Info, archived bool, current string) httpapi.SessionInfo {
	return httpapi.SessionInfo{
		Path:         info.Path,
		ID:           info.ID,
		Name:         info.Name,
		Cwd:          info.Cwd,
		Created:      stamp(info.Created),
		Modified:     stamp(info.Modified),
		Messages:     info.MessageCount,
		FirstMessage: info.FirstMessage,
		Archived:     archived,
		Current:      info.Path == current,
	}
}

// spellings returns a path and the same directory with the symlinks resolved.
func spellings(path string) []string {
	out := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		out = append(out, resolved)
	}
	return out
}

// sameDir compares two directories by identity, falling back to the strings for
// a path that no longer exists.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	left, errLeft := filepath.EvalSymlinks(a)
	right, errRight := filepath.EvalSymlinks(b)
	if errLeft != nil || errRight != nil {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// missing reports whether a project's directory is gone. Its conversations are
// still drawn: the files outlive the directory, and hiding them would look like
// they were deleted with it.
func missing(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	return !info.IsDir()
}

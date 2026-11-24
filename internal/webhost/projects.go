package webhost

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pi-go/internal/config"
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
	stored := h.projects.List()
	groups := mergeGroups(h.defaultDir(), stored)
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
			Name:     projectName(path, stored),
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
	return h.AddNamedProject(path, "")
}

// AddNamedProject remembers a folder with an optional display name.
func (h *Host) AddNamedProject(path, name string) error {
	_, err := h.projects.AddNamed(path, name)
	return err
}

// RemoveProject forgets a folder. Its conversations stay on disk, and they come
// back the moment the folder is added again.
func (h *Host) RemoveProject(path string) error {
	return h.projects.Remove(path)
}

// NewSessionIn starts a conversation in a folder the reader can see, including
// leaving a read-only archived conversation that the reader was viewing.
//
// The list is what makes a directory reachable from the window, so a folder
// outside it is refused: the page must not be able to start writing session
// files anywhere it likes.
func (h *Host) NewSessionIn(cwd string) error {
	if !h.known(cwd) {
		return fmt.Errorf("webhost: %s is not the default folder or a project", cwd)
	}
	if !h.kernel.CanSpawnSessions() {
		slot := h.activeSlot()
		oldKey := h.activeKey
		if err := slot.live.App.NewSessionInLeavingArchived(cwd); err != nil {
			return err
		}
		h.mu.Lock()
		h.rekeyActiveSlotLocked(oldKey, slot)
		h.mu.Unlock()
		return nil
	}
	live, err := h.kernel.OpenLiveSession("", cwd)
	if err != nil {
		return err
	}
	h.activateSlot(live)
	return nil
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
		if !project.IsLocalDevice(entry.DeviceID) {
			continue
		}
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

func projectName(path string, stored []project.Project) string {
	for _, entry := range stored {
		if !project.IsLocalDevice(entry.DeviceID) {
			continue
		}
		if sameDir(entry.Path, path) {
			if name := strings.TrimSpace(entry.Name); name != "" {
				return name
			}
			break
		}
	}
	return filepath.Base(path)
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
func (h *Host) defaultDir() string { return h.startupDir() }

// sessionsIn lists one directory's conversations.
//
// The bucket is asked for both the path as written and the same directory with
// its symlinks resolved: a picker hands back the resolved form ("/private/tmp"
// for "/tmp") while the header records what the process was told, and the bucket
// name is derived from the string. Both answers are then filtered by directory
// identity, so a bucket two projects happen to share contributes only its own.
func (h *Host) sessionsIn(cwd string, archived bool) ([]httpapi.SessionInfo, error) {
	active := h.activeSlot().live.App
	current := active.Session().File()
	list := session.List
	if archived {
		list = session.ListArchived
	}
	seen := map[string]bool{}
	out := []httpapi.SessionInfo{}
	for _, candidate := range spellings(cwd) {
		dir, err := active.SessionDir(candidate)
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
		Current:      sameSessionFile(info.Path, current),
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

// BrowseDirs lists subdirectories inside a local path for the Add Project
// dialog. Remote devices are refused here and handled by the remote router.
func (h *Host) BrowseDirs(deviceID, path string, includeHidden bool) (httpapi.DirBrowseResult, error) {
	if !project.IsLocalDevice(deviceID) {
		return httpapi.DirBrowseResult{}, fmt.Errorf("webhost: cannot browse remote device %q", deviceID)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = h.defaultDir()
	}
	target := strings.TrimSpace(path)
	switch {
	case target == "", target == "~":
		target = home
	case strings.HasPrefix(target, "~/"):
		target = filepath.Join(home, strings.TrimPrefix(target, "~/"))
	}
	if !filepath.IsAbs(target) {
		if abs, absErr := filepath.Abs(target); absErr == nil {
			target = abs
		}
	}
	target = filepath.Clean(target)
	info, err := os.Stat(target)
	if err != nil {
		return httpapi.DirBrowseResult{}, fmt.Errorf("webhost: browse %s: %w", target, err)
	}
	if !info.IsDir() {
		return httpapi.DirBrowseResult{}, fmt.Errorf("webhost: browse %s: not a directory", target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return httpapi.DirBrowseResult{}, fmt.Errorf("webhost: browse %s: %w", target, err)
	}
	dirs := make([]httpapi.BrowseDirEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		isDir := entry.IsDir()
		if !isDir && entry.Type()&os.ModeSymlink != 0 {
			if targetInfo, statErr := os.Stat(filepath.Join(target, name)); statErr == nil && targetInfo.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		dirs = append(dirs, httpapi.BrowseDirEntry{
			Name: name,
			Path: filepath.Join(target, name),
		})
	}
	sort.Slice(dirs, func(i, j int) bool {
		li := strings.ToLower(dirs[i].Name)
		lj := strings.ToLower(dirs[j].Name)
		if li == lj {
			return dirs[i].Name < dirs[j].Name
		}
		return li < lj
	})
	parent := filepath.Dir(target)
	if parent == target {
		parent = ""
	}
	return httpapi.DirBrowseResult{
		DeviceID: project.LocalDeviceID,
		Path:     target,
		Parent:   parent,
		Home:     home,
		Entries:  dirs,
	}, nil
}

// DiscoverProjects collects tracked local projects and directories that already
// hold historical sessions on this host.
func (h *Host) DiscoverProjects(deviceID string) ([]httpapi.DiscoveredProject, error) {
	if !project.IsLocalDevice(deviceID) {
		return nil, fmt.Errorf("webhost: cannot discover projects on remote device %q", deviceID)
	}
	stored := h.projects.List()
	addedPaths := make([]string, 0, len(stored))
	addedNames := map[string]string{}
	for _, entry := range stored {
		if !project.IsLocalDevice(entry.DeviceID) {
			continue
		}
		addedPaths = append(addedPaths, entry.Path)
		addedNames[entry.Path] = entry.Name
	}

	type acc struct {
		path    string
		name    string
		count   int
		lastMod string
		added   bool
	}
	byKey := map[string]*acc{}
	order := []string{}
	addCandidate := func(rawPath, name string, added bool) *acc {
		clean := filepath.Clean(rawPath)
		if clean == "" || clean == "." {
			return nil
		}
		for _, k := range order {
			if sameDir(k, clean) {
				if added {
					byKey[k].added = true
				}
				if strings.TrimSpace(name) != "" && byKey[k].name == filepath.Base(byKey[k].path) {
					byKey[k].name = strings.TrimSpace(name)
				}
				return byKey[k]
			}
		}
		display := strings.TrimSpace(name)
		if display == "" {
			display = filepath.Base(clean)
		}
		item := &acc{path: clean, name: display, added: added}
		byKey[clean] = item
		order = append(order, clean)
		return item
	}

	for _, p := range addedPaths {
		addCandidate(p, addedNames[p], true)
	}

	if home, err := config.DefaultHome(); err == nil && home != "" {
		if infos, err := session.ListAll(home); err == nil {
			for _, info := range infos {
				if strings.TrimSpace(info.Cwd) == "" || missing(info.Cwd) {
					continue
				}
				item := addCandidate(info.Cwd, "", false)
				if item == nil {
					continue
				}
				item.count++
				mod := stamp(info.Modified)
				if mod > item.lastMod {
					item.lastMod = mod
				}
			}
		}
	}

	out := make([]httpapi.DiscoveredProject, 0, len(order))
	for _, key := range order {
		item := byKey[key]
		if item.count == 0 {
			if infos, err := h.sessionsIn(item.path, false); err == nil {
				item.count = len(infos)
				if len(infos) > 0 && infos[0].Modified > item.lastMod {
					item.lastMod = infos[0].Modified
				}
			}
		}
		out = append(out, httpapi.DiscoveredProject{
			DeviceID:     project.LocalDeviceID,
			Path:         item.path,
			Name:         item.name,
			SessionCount: item.count,
			LastModified: item.lastMod,
			Added:        item.added,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastModified != out[j].LastModified {
			return out[i].LastModified > out[j].LastModified
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// Package project stores the folders a window shows as projects.
//
// A project is a directory the reader picked and nothing more. The
// conversations under it are the session files whose header records that
// directory, so adding one shows what is already on disk and removing one never
// touches a file.
//
// The list is kept beside the config rather than in it. config.json is parsed
// strictly and shared with the command line, which has no projects: a field
// there would be validated by a front end that never reads it, and a list that
// went bad would refuse a config the terminal is happy with. A separate file
// also makes the failure mode cheap — an unreadable list is an empty list.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	posixpath "path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileName is the list's name inside the atlas home.
const FileName = "projects.json"

// LocalDeviceID names the local machine in project records and API calls.
const LocalDeviceID = "local"

// IsLocalDevice reports whether deviceID refers to the local machine.
func IsLocalDevice(deviceID string) bool {
	trimmed := strings.TrimSpace(deviceID)
	return trimmed == "" || trimmed == LocalDeviceID
}

// Project is one folder in the list.
//
// Name is an optional display label set by the user; when empty, front ends
// fall back to the directory's basename. DeviceID identifies the remote SSH
// device when non-empty (empty means local). Added is when it was picked, and
// it doubles as the order the rail draws: a list the reader builds grows at the
// end.
type Project struct {
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	Added    string `json:"added,omitempty"`
}

// file is the on-disk shape. An object rather than a bare array so a later
// field has somewhere to go without rewriting every file.
type file struct {
	Projects []Project `json:"projects"`
}

// Store is one list on disk.
//
// Every read takes the file again instead of caching it: the list is a handful
// of lines, and a second window (or the reader's editor) can change it between
// two calls.
type Store struct {
	mu   sync.Mutex
	path string
}

// Open names the list. The file does not have to exist.
func Open(path string) *Store { return &Store{path: path} }

// Path is where the list lives, for a front end that wants to say so.
func (s *Store) Path() string { return s.path }

// List returns the projects in the order they were added.
//
// A file that is missing is an empty list, and so is one that cannot be parsed:
// this is a convenience the reader assembles by hand-editing a JSON file, and a
// window that refused to open because of a stray comma would be worse than one
// that opens with no projects.
func (s *Store) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Has reports whether local path is already listed, comparing directories
// rather than strings so "/x" and "/x/" are the same project.
func (s *Store) Has(path string) bool {
	resolved, err := resolve(path)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.load() {
		if IsLocalDevice(entry.DeviceID) && sameDir(entry.Path, resolved) {
			return true
		}
	}
	return false
}

// Add appends a local folder. The path is resolved, the directory has to exist,
// and a folder already on the list is returned as it stands rather than twice.
func (s *Store) Add(path string) (Project, error) {
	return s.AddNamed(path, "")
}

// AddNamed appends a local folder with an optional display name. If the folder
// is already listed and name is non-empty, its stored display name is updated.
func (s *Store) AddNamed(path, name string) (Project, error) {
	resolved, err := resolve(path)
	if err != nil {
		return Project{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return Project{}, fmt.Errorf("project: %s does not exist", resolved)
		}
		return Project{}, fmt.Errorf("project: stat %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return Project{}, fmt.Errorf("project: %s is not a directory", resolved)
	}

	label := strings.TrimSpace(name)

	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	for i, entry := range list {
		if IsLocalDevice(entry.DeviceID) && sameDir(entry.Path, resolved) {
			if label != "" && entry.Name != label {
				list[i].Name = label
				if err := s.save(list); err != nil {
					return Project{}, err
				}
			}
			return list[i], nil
		}
	}
	added := Project{Path: resolved, Name: label, Added: time.Now().UTC().Format(time.RFC3339)}
	list = append(list, added)
	if err := s.save(list); err != nil {
		return Project{}, err
	}
	return added, nil
}

// AddRemote appends a project directory on a remote device without probing the
// local filesystem. Paths are normalized using POSIX rules; validation and home
// expansion belong to the remote host.
func (s *Store) AddRemote(deviceID, remotePath, name string) (Project, error) {
	dev := strings.TrimSpace(deviceID)
	if IsLocalDevice(dev) {
		return Project{}, fmt.Errorf("project: invalid remote device id %q", deviceID)
	}
	cleaned, err := CleanRemotePath(remotePath)
	if err != nil {
		return Project{}, err
	}
	label := strings.TrimSpace(name)

	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	for i, entry := range list {
		if entry.DeviceID == dev && sameRemotePath(entry.Path, cleaned) {
			if label != "" && entry.Name != label {
				list[i].Name = label
				if err := s.save(list); err != nil {
					return Project{}, err
				}
			}
			return list[i], nil
		}
	}
	added := Project{
		Path:     cleaned,
		Name:     label,
		DeviceID: dev,
		Added:    time.Now().UTC().Format(time.RFC3339),
	}
	list = append(list, added)
	if err := s.save(list); err != nil {
		return Project{}, err
	}
	return added, nil
}

// Remove drops one local folder from the list. The conversations stay where
// they are: this is a list of shortcuts, not a directory of files this package
// owns.
func (s *Store) Remove(path string) error {
	return s.RemoveForDevice("", path)
}

// RemoveForDevice drops a project scoped by deviceID (empty or "local" for
// local projects).
func (s *Store) RemoveForDevice(deviceID, path string) error {
	dev := strings.TrimSpace(deviceID)
	if IsLocalDevice(dev) {
		resolved, err := resolve(path)
		if err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		list := s.load()
		kept := make([]Project, 0, len(list))
		removed := false
		for _, entry := range list {
			if IsLocalDevice(entry.DeviceID) && sameDir(entry.Path, resolved) {
				removed = true
				continue
			}
			kept = append(kept, entry)
		}
		if !removed {
			return fmt.Errorf("project: %s is not in the list", resolved)
		}
		return s.save(kept)
	}

	cleaned, err := CleanRemotePath(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	kept := make([]Project, 0, len(list))
	removed := false
	for _, entry := range list {
		if entry.DeviceID == dev && sameRemotePath(entry.Path, cleaned) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return fmt.Errorf("project: %s (%s) is not in the list", cleaned, dev)
	}
	return s.save(kept)
}

// RemoveAllForDevice drops every project entry associated with deviceID.
func (s *Store) RemoveAllForDevice(deviceID string) error {
	dev := strings.TrimSpace(deviceID)
	if IsLocalDevice(dev) {
		return fmt.Errorf("project: refuse bulk removal of local projects")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	kept := make([]Project, 0, len(list))
	changed := false
	for _, entry := range list {
		if entry.DeviceID == dev {
			changed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !changed {
		return nil
	}
	return s.save(kept)
}

// CleanRemotePath normalizes a POSIX remote project path without inspecting
// the local filesystem.
func CleanRemotePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("project: empty path")
	}
	if !strings.HasPrefix(trimmed, "/") && trimmed != "~" && !strings.HasPrefix(trimmed, "~/") {
		return "", fmt.Errorf("project: remote path %q must be absolute or start with ~/", raw)
	}
	return posixpath.Clean(trimmed), nil
}

func sameRemotePath(a, b string) bool {
	ca, errA := CleanRemotePath(a)
	cb, errB := CleanRemotePath(b)
	if errA != nil || errB != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return ca == cb
}

// load reads the file. Callers hold the lock.
func (s *Store) load() []Project {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var decoded file
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil
	}
	kept := make([]Project, 0, len(decoded.Projects))
	for _, entry := range decoded.Projects {
		if strings.TrimSpace(entry.Path) == "" {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// save replaces the file in one step, so a crash leaves the old list or the new
// one and never half of either.
func (s *Store) save(list []Project) error {
	data, err := json.MarshalIndent(file{Projects: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("project: encode: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("project: create directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".projects-*.json")
	if err != nil {
		return fmt.Errorf("project: write: %w", err)
	}
	name := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		os.Remove(name)
		return fmt.Errorf("project: write: %w", err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("project: write: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("project: replace %s: %w", s.path, err)
	}
	return nil
}

// resolve expands "~" and makes a path absolute, without following symlinks:
// the stored string is what the reader picked, and the comparisons below are
// the place a symlink is resolved.
func resolve(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("project: empty path")
	}
	expanded := trimmed
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("project: locate the home directory: %w", err)
		}
		if trimmed == "~" {
			expanded = home
		} else {
			expanded = filepath.Join(home, filepath.FromSlash(trimmed[2:]))
		}
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("project: resolve %s: %w", path, err)
	}
	return absolute, nil
}

// sameDir reports whether two paths name the same directory.
//
// A picker hands back a path with symlinks already followed ("/tmp" becomes
// "/private/tmp" on macOS) while a session header records the directory the
// process ran in, so comparing the strings alone would hide a project's own
// conversations from it. Both sides are resolved before comparing, and the
// strings remain the fallback for a path that no longer exists.
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

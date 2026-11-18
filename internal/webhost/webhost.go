// Package webhost adapts a built kernel to the surface a window asks for.
//
// httpapi deliberately knows nothing about providers, session files or a model
// catalog; assembly knows nothing about HTTP. This package is the only place
// that holds both, which is what keeps either of them from growing a branch for
// the other. Every method is a translation and nothing else: if a decision
// belongs to the kernel, the kernel makes it here.
package webhost

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/httpapi"
	"pi-go/internal/permission"
	"pi-go/internal/project"
	"pi-go/internal/session"
)

// maxFileBytes bounds one file read. The window draws text, and a compiled
// binary is not text: past this size the answer is a truncated preview rather
// than a hang in the browser.
const maxFileBytes = 1 << 20

// Host is the kernel behind a window.
type Host struct {
	kernel   *assembly.Kernel
	projects *project.Store
}

// Options is what a window needs beyond the kernel.
type Options struct {
	// Projects is the file the folder list is read from and written to. Empty
	// means <atlas home>/projects.json, which is what the window uses.
	Projects string
}

// New wraps a kernel. The kernel must outlive the Host.
func New(kernel *assembly.Kernel) *Host { return NewWith(kernel, Options{}) }

// NewWith wraps a kernel with explicit paths, which is what a test needs.
func NewWith(kernel *assembly.Kernel, options Options) *Host {
	file := options.Projects
	if file == "" {
		if home, err := config.DefaultHome(); err == nil {
			file = filepath.Join(home, project.FileName)
		}
	}
	return &Host{kernel: kernel, projects: project.Open(file)}
}

// root is the directory file reads are confined to.
func (h *Host) root() string {
	if h.kernel.Cwd != "" {
		return h.kernel.Cwd
	}
	directory, err := os.Getwd()
	if err != nil {
		return "."
	}
	return directory
}

// Sessions lists conversations from the directory this kernel stores them in.
// The current one is marked rather than filtered out: a picker that hides where
// the reader already is makes switching back feel like opening a new file.
func (h *Host) Sessions(archived bool) ([]httpapi.SessionInfo, error) {
	manager := h.kernel.App.Session()
	directory, cwd := manager.Dir(), manager.Cwd()
	list := session.List
	if archived {
		list = session.ListArchived
	}
	infos, err := list(directory, cwd)
	if err != nil {
		return nil, err
	}
	current := manager.File()
	out := make([]httpapi.SessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, h.sessionInfo(info, archived, current))
	}
	return out, nil
}

// OpenSession switches to a conversation already on disk.
func (h *Host) OpenSession(path string) error {
	return h.kernel.App.SwitchSession(path)
}

// NewSession starts an empty conversation.
func (h *Host) NewSession() error {
	return h.kernel.App.NewSession()
}

// ArchiveSession hides one conversation from the picker.
func (h *Host) ArchiveSession(path string) error {
	return session.Archive(path)
}

// RestoreSession brings an archived conversation back.
func (h *Host) RestoreSession(path string) error {
	return session.Unarchive(path)
}

// DeleteSession removes the file behind a conversation. The kernel is refused
// the delete of the session it is holding: removing the file under an open
// manager would leave the window writing to a handle whose directory entry is
// gone.
func (h *Host) DeleteSession(path string) error {
	if path == h.kernel.App.Session().File() {
		return fmt.Errorf("webhost: refusing to delete the open session")
	}
	return session.DeleteFile(path)
}

// Models reports the catalog plus what is in effect.
func (h *Host) Models() (httpapi.ModelList, error) {
	options := h.kernel.App.Models()
	current := h.kernel.App.Model()
	levels, _ := h.kernel.App.AvailableThinkingLevels()
	// A model with no reasoning axis has no level to report. Saying "medium"
	// beside a name with no control to change it would be a setting the reader
	// cannot act on, and the adapters send nothing for such a model anyway.
	thinking := ""
	if len(levels) > 0 {
		thinking = h.kernel.App.ThinkingLevel()
	}
	list := httpapi.ModelList{
		Current:  current.Provider + "/" + current.ID,
		Thinking: thinking,
		Levels:   levels,
		Models:   make([]httpapi.ModelOption, 0, len(options)),
	}
	for _, option := range options {
		list.Models = append(list.Models, httpapi.ModelOption{
			Provider:      option.Model.Provider,
			ID:            option.Model.ID,
			Name:          option.Model.Name,
			Reasoning:     option.Model.Reasoning,
			ContextWindow: option.Model.ContextWindow,
			Levels:        option.Levels,
			Current:       option.Model.Provider == current.Provider && option.Model.ID == current.ID,
		})
	}
	return list, nil
}

// SetModel switches the model used by later turns.
func (h *Host) SetModel(provider, id string) error {
	_, err := h.kernel.App.SetModel(provider, id)
	return err
}

// SetThinking switches the reasoning level used by later turns.
func (h *Host) SetThinking(level string) error {
	return h.kernel.App.SetThinkingLevel(level)
}

// Permission reports the gate's posture and the four rows that describe it.
//
// The rows are the terminal's own /permission menu, read from the same table in
// internal/permission: the window shows "Full access" because the CLI shows
// "Full access", not because a second list happened to be typed the same way.
func (h *Host) Permission() (httpapi.PermissionState, error) {
	engine := h.kernel.Gate.Engine()
	if engine == nil {
		return httpapi.PermissionState{}, fmt.Errorf("permission: this kernel has no permission engine")
	}
	// An engine built without a mode reports "", which is ModeDefault's own
	// spelling. Sending "" would draw four rows and highlight none of them.
	mode := engine.Mode()
	if mode == "" {
		mode = permission.ModeDefault
	}
	state := httpapi.PermissionState{
		Mode:  mode,
		Modes: make([]httpapi.PermissionMode, 0, len(config.PermissionModes)),
	}
	for _, value := range config.PermissionModes {
		state.Modes = append(state.Modes, httpapi.PermissionMode{
			Value: value,
			Label: permission.Label(value),
			Note:  permission.Note(value),
		})
	}
	return state, nil
}

// SetPermission switches the posture for the rest of the process.
//
// It is the runtime half of the four modes and the same call /permission makes,
// so a switch in the window takes effect on the next tool call in either front
// end rather than on the next session. The kernel refuses an unknown name; that
// refusal is passed on instead of being flattened into "nothing happened".
func (h *Host) SetPermission(mode string) error {
	engine := h.kernel.Gate.Engine()
	if engine == nil {
		return fmt.Errorf("permission: this kernel has no permission engine")
	}
	return engine.SetMode(mode)
}

// Stats totals the session.
func (h *Host) Stats() (httpapi.Stats, error) {
	stats, err := h.kernel.App.SessionStats()
	if err != nil {
		return httpapi.Stats{}, err
	}
	out := httpapi.Stats{
		SessionID:         stats.SessionID,
		SessionFile:       stats.SessionFile,
		UserMessages:      stats.UserMessages,
		AssistantMessages: stats.AssistantMessages,
		ToolCalls:         stats.ToolCalls,
		TotalMessages:     stats.TotalMessages,
		InputTokens:       int64(stats.Tokens.Input),
		OutputTokens:      int64(stats.Tokens.Output),
		TotalTokens:       int64(stats.Tokens.Total),
		Cost:              stats.Cost,
	}
	if stats.ContextUsage != nil {
		out.ContextWindow = stats.ContextUsage.ContextWindow
		if stats.ContextUsage.Tokens != nil {
			out.ContextTokens = *stats.ContextUsage.Tokens
		}
	}
	return out, nil
}

// ReadDir lists one directory below the working directory.
func (h *Host) ReadDir(path string) (httpapi.DirListing, error) {
	full, relative, err := h.resolve(path)
	if err != nil {
		return httpapi.DirListing{}, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return httpapi.DirListing{}, err
	}
	listing := httpapi.DirListing{Path: relative, Entries: make([]httpapi.FileEntry, 0, len(entries))}
	for _, entry := range entries {
		name := entry.Name()
		// Dotfiles stay hidden for the same reason a shell hides them: a
		// directory listing is for the files the reader is working on, and .git
		// is never one of them.
		if strings.HasPrefix(name, ".") {
			continue
		}
		item := httpapi.FileEntry{
			Name:  name,
			Path:  filepath.ToSlash(filepath.Join(relative, name)),
			IsDir: entry.IsDir(),
		}
		if info, err := entry.Info(); err == nil && !entry.IsDir() {
			item.Size = info.Size()
		}
		listing.Entries = append(listing.Entries, item)
	}
	sort.SliceStable(listing.Entries, func(i, j int) bool {
		if listing.Entries[i].IsDir != listing.Entries[j].IsDir {
			return listing.Entries[i].IsDir
		}
		return strings.ToLower(listing.Entries[i].Name) < strings.ToLower(listing.Entries[j].Name)
	})
	return listing, nil
}

// ReadFile returns one file below the working directory.
func (h *Host) ReadFile(path string) (httpapi.FileContent, error) {
	full, relative, err := h.resolve(path)
	if err != nil {
		return httpapi.FileContent{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return httpapi.FileContent{}, err
	}
	if info.IsDir() {
		return httpapi.FileContent{}, fmt.Errorf("webhost: %s is a directory", relative)
	}
	file, err := os.Open(full)
	if err != nil {
		return httpapi.FileContent{}, err
	}
	defer file.Close()

	// One byte past the cap is enough to know it was cut.
	buffer := make([]byte, maxFileBytes+1)
	n, err := file.Read(buffer)
	if err != nil && n == 0 {
		return httpapi.FileContent{}, err
	}
	truncated := n > maxFileBytes
	if truncated {
		n = maxFileBytes
	}
	content := httpapi.FileContent{Path: relative, Size: info.Size(), Truncated: truncated}
	if isBinary(buffer[:n]) {
		content.Binary = true
		return content, nil
	}
	content.Text = string(buffer[:n])
	return content, nil
}

// resolve turns a frontend path into an absolute one and refuses anything that
// would leave the working directory.
func (h *Host) resolve(path string) (string, string, error) {
	root, err := filepath.Abs(h.root())
	if err != nil {
		return "", "", err
	}
	relative, err := confine(root, path)
	if err != nil {
		return "", "", err
	}
	if relative == "" {
		return root, "", nil
	}
	return filepath.Join(root, filepath.FromSlash(relative)), relative, nil
}

// confine cleans a frontend path against a root and reports the relative form.
//
// The guarantee is containment, not rejection: the path gets a leading slash
// before cleaning, so "../.." climbs to the root and stops there and
// "a/../../etc/passwd" normalises to "etc/passwd" inside it. Containing is
// deliberately better than erroring — the window never has to reason about
// which of its paths were refused. The ".." branch below is therefore a guard
// the anchoring already makes unreachable; it stays because the anchoring is
// the line that could be dropped by accident.
func confine(root, path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash("/" + path))
	full := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("webhost: %s is outside the working directory", path)
	}
	if relative == "." {
		return "", nil
	}
	return filepath.ToSlash(relative), nil
}

// isBinary reports whether the first bytes look like something a text editor
// should not receive. A NUL byte in the head is the cheap, adequate test.
func isBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

func stamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

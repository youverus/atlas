package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// Host is what a full window needs beyond a single turn: the sessions it can
// switch to, the models it can pick, and the directory the tools work in.
//
// It is an interface for the same reason Turn is one. This package owns a
// transport, not an agent: it must not learn what a provider or a session file
// is, or the loopback layer becomes the place every shell feature has to be
// ported to. The composition root implements this over the real kernel.
//
// Every method returns an error rather than panicking: a window that asks for a
// session that has been deleted should be told so, not dropped.
type Host interface {
	// Sessions lists conversations, newest first. archived selects the other
	// list rather than adding to this one, because a picker and a recovery
	// screen are different questions.
	Sessions(archived bool) ([]SessionInfo, error)
	// OpenSession switches the kernel to a conversation already on disk.
	OpenSession(path string) error
	// NewSession abandons the current conversation and starts an empty one.
	NewSession() error
	// ArchiveSession hides a conversation from the picker without deleting it.
	ArchiveSession(path string) error
	// RestoreSession undoes ArchiveSession.
	RestoreSession(path string) error
	// DeleteSession removes the file.
	DeleteSession(path string) error

	// Projects lists the folders a window draws, each with its conversations.
	// archived selects the other list, the same way Sessions does.
	Projects(archived bool) (ProjectList, error)
	// AddProject remembers a folder. Validation belongs to the Host: only it
	// knows which directories a conversation may run in.
	AddProject(path string) error
	// RemoveProject forgets a folder without touching its conversations.
	RemoveProject(path string) error
	// NewSessionIn starts an empty conversation in another project.
	NewSessionIn(cwd string) error

	// Models is the catalog the kernel will accept, plus what is in effect.
	Models() (ModelList, error)
	// SetModel switches the provider and model used by later turns.
	SetModel(provider, id string) error
	// SetThinking switches the reasoning level used by later turns.
	SetThinking(level string) error

	// Stats totals the session and reports the live context window.
	Stats() (Stats, error)
	// ReadDir lists one directory under the working directory.
	ReadDir(path string) (DirListing, error)
	// ReadFile returns one file under the working directory.
	ReadFile(path string) (FileContent, error)
}

// Picker is the native folder chooser.
//
// It is separate from Host because only a desktop shell has one: the same page
// served to a browser has no dialog to open, and saying so with 501 is what
// lets the page offer a path field instead of a button that does nothing.
type Picker interface {
	// PickFolder returns the chosen directory, or "" when the reader cancelled.
	PickFolder() (string, error)
}

// ProjectGroup is one folder in the rail: a project and its conversations.
//
// Default marks the folder a conversation starts in when the reader has not
// picked another one. It is always drawn — a rail whose only group vanished
// with the last project would leave the reader no way to start anything.
type ProjectGroup struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Default is the folder conversations start in when nothing else is chosen.
	Default bool `json:"default,omitempty"`
	// Missing marks a folder that was removed from under the list. Its
	// conversations are still shown: the files outlive the directory, and
	// hiding them would look like they were deleted.
	Missing  bool          `json:"missing,omitempty"`
	Sessions []SessionInfo `json:"sessions"`
}

// ProjectList is the whole rail: every group, in the order it is drawn.
type ProjectList struct {
	Groups []ProjectGroup `json:"groups"`
}

// SessionInfo is one row of the session list. It is a flat copy of the fields a
// picker draws, so the frontend never parses a session file.
type SessionInfo struct {
	Path         string `json:"path"`
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Created      string `json:"created,omitempty"`
	Modified     string `json:"modified,omitempty"`
	Messages     int    `json:"messages"`
	FirstMessage string `json:"firstMessage,omitempty"`
	Archived     bool   `json:"archived,omitempty"`
	Current      bool   `json:"current,omitempty"`
}

// ModelOption is one selectable model. Levels is empty for a model that does
// not reason, which is what lets the picker hide the thinking control instead
// of offering a choice that would be rejected.
type ModelOption struct {
	Provider      string   `json:"provider"`
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	Levels        []string `json:"levels,omitempty"`
	Current       bool     `json:"current,omitempty"`
}

// ModelList is the whole model surface in one answer: there is no second call
// to make, because a picker with a model list and no current model is a picker
// that cannot draw its own selection.
type ModelList struct {
	Current  string        `json:"current"`
	Thinking string        `json:"thinking,omitempty"`
	Levels   []string      `json:"levels,omitempty"`
	Models   []ModelOption `json:"models"`
}

// Stats is the usage readout. Zero values mean "not counted", which is why the
// context fields are separate: a session with no recorded usage still has a
// window.
type Stats struct {
	SessionID         string  `json:"sessionId,omitempty"`
	SessionFile       string  `json:"sessionFile,omitempty"`
	UserMessages      int     `json:"userMessages"`
	AssistantMessages int     `json:"assistantMessages"`
	ToolCalls         int     `json:"toolCalls"`
	TotalMessages     int     `json:"totalMessages"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	TotalTokens       int64   `json:"totalTokens"`
	Cost              float64 `json:"cost"`
	ContextTokens     int     `json:"contextTokens"`
	ContextWindow     int     `json:"contextWindow"`
}

// FileEntry is one row of a directory listing. Path is relative to the working
// directory, so the frontend can send it back without knowing the root.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size,omitempty"`
}

// DirListing is one directory.
type DirListing struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// FileContent is one file. Binary is set when the bytes are not text, which is
// a rendering decision the transport is allowed to make because it already has
// the bytes; Truncated marks a file cut at the size limit.
type FileContent struct {
	Path      string `json:"path"`
	Text      string `json:"text"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// mountHost registers the routes that need a Host. A server without one keeps
// serving the single-conversation page, which is what the tests and the
// -offline path use.
func (s *Server) mountHost() {
	s.mux.HandleFunc("GET /api/sessions", s.serveSessions)
	s.mux.HandleFunc("POST /api/sessions/open", s.serveSessionOpen)
	s.mux.HandleFunc("POST /api/sessions/new", s.serveSessionNew)
	s.mux.HandleFunc("POST /api/sessions/archive", s.serveSessionArchive)
	s.mux.HandleFunc("POST /api/sessions/restore", s.serveSessionRestore)
	s.mux.HandleFunc("POST /api/sessions/delete", s.serveSessionDelete)
	s.mux.HandleFunc("GET /api/projects", s.serveProjects)
	s.mux.HandleFunc("POST /api/projects", s.serveProjectAdd)
	s.mux.HandleFunc("POST /api/projects/remove", s.serveProjectRemove)
	s.mux.HandleFunc("POST /api/projects/pick", s.serveProjectPick)
	s.mux.HandleFunc("GET /api/models", s.serveModels)
	s.mux.HandleFunc("POST /api/models", s.serveModelSet)
	s.mux.HandleFunc("POST /api/thinking", s.serveThinkingSet)
	s.mux.HandleFunc("GET /api/stats", s.serveStats)
	s.mux.HandleFunc("GET /api/files", s.serveFiles)
	s.mux.HandleFunc("GET /api/file", s.serveFile)
}

// needHost refuses a request that arrived without a Host configured. 501 rather
// than 404: the route exists, this build just cannot answer it.
func (s *Server) needHost(w http.ResponseWriter) bool {
	if s.host != nil {
		return true
	}
	http.Error(w, "httpapi: no host configured", http.StatusNotImplemented)
	return false
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, err error, status int) {
	http.Error(w, err.Error(), status)
}

func (s *Server) serveSessions(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	archived, _ := strconv.ParseBool(r.URL.Query().Get("archived"))
	list, err := s.host.Sessions(archived)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []SessionInfo{}
	}
	writeJSON(w, list)
}

// sessionRequest is the body of every session command: one path, because every
// one of them names an existing conversation.
type sessionRequest struct {
	Path string `json:"path"`
}

func (s *Server) decodeSession(w http.ResponseWriter, r *http.Request) (sessionRequest, bool) {
	var request sessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return request, false
	}
	if request.Path == "" {
		http.Error(w, "httpapi: empty path", http.StatusBadRequest)
		return request, false
	}
	return request, true
}

func (s *Server) serveSessionOpen(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	request, ok := s.decodeSession(w, r)
	if !ok {
		return
	}
	if err := s.host.OpenSession(request.Path); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishStatus(statusEvent{Running: s.running.Load()})
	writeJSON(w, map[string]bool{"ok": true})
}

// newSessionRequest is the body of /api/sessions/new. Cwd is optional: the
// primary button starts a conversation where the last one ran, and a project's
// own button names the folder it belongs to.
type newSessionRequest struct {
	Cwd string `json:"cwd"`
}

func (s *Server) serveSessionNew(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request newSessionRequest
	// An empty body is the ordinary "new conversation here": the page sends
	// nothing rather than a body that repeats the current directory.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var err error
	if request.Cwd == "" {
		err = s.host.NewSession()
	} else {
		err = s.host.NewSessionIn(request.Cwd)
	}
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishStatus(statusEvent{Running: s.running.Load()})
	writeJSON(w, map[string]bool{"ok": true})
}

// serveProjects draws the rail: every folder with its conversations.
func (s *Server) serveProjects(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	archived, _ := strconv.ParseBool(r.URL.Query().Get("archived"))
	list, err := s.host.Projects(archived)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if list.Groups == nil {
		list.Groups = []ProjectGroup{}
	}
	writeJSON(w, list)
}

func (s *Server) serveProjectAdd(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	request, ok := s.decodeSession(w, r)
	if !ok {
		return
	}
	if err := s.host.AddProject(request.Path); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveProjectRemove(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	request, ok := s.decodeSession(w, r)
	if !ok {
		return
	}
	if err := s.host.RemoveProject(request.Path); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// serveProjectPick opens the shell's own folder chooser. A build without one
// answers 501 on purpose: the page then knows to ask for a path instead of
// showing a button that cannot work.
func (s *Server) serveProjectPick(w http.ResponseWriter, r *http.Request) {
	if s.picker == nil {
		http.Error(w, "httpapi: this build has no folder chooser", http.StatusNotImplemented)
		return
	}
	path, err := s.picker.PickFolder()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

func (s *Server) serveSessionArchive(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	request, ok := s.decodeSession(w, r)
	if !ok {
		return
	}
	if err := s.host.ArchiveSession(request.Path); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveSessionRestore(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	request, ok := s.decodeSession(w, r)
	if !ok {
		return
	}
	if err := s.host.RestoreSession(request.Path); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveSessionDelete(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	request, ok := s.decodeSession(w, r)
	if !ok {
		return
	}
	if err := s.host.DeleteSession(request.Path); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveModels(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	list, err := s.host.Models()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

// modelRequest names a model the way a session file and a flag do.
type modelRequest struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

func (s *Server) serveModelSet(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request modelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Provider == "" || request.ID == "" {
		http.Error(w, "httpapi: provider and id are required", http.StatusBadRequest)
		return
	}
	if err := s.host.SetModel(request.Provider, request.ID); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveThinkingSet(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Level == "" {
		http.Error(w, "httpapi: empty level", http.StatusBadRequest)
		return
	}
	if err := s.host.SetThinking(request.Level); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveStats(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	stats, err := s.host.Stats()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *Server) serveFiles(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	listing, err := s.host.ReadDir(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if listing.Entries == nil {
		listing.Entries = []FileEntry{}
	}
	writeJSON(w, listing)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "httpapi: empty path", http.StatusBadRequest)
		return
	}
	content, err := s.host.ReadFile(path)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, content)
}

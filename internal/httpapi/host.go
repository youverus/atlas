package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"pi-go/internal/wire"
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
// session that has been deleted should be told so, not dropped. ForkPoints is
// the one exception, because it only reads entries already in memory and so has
// nothing to report; giving it an error nobody can return would be ceremony.
type Host interface {
	// Sessions lists conversations, newest first. archived selects the other
	// list rather than adding to this one, because a picker and a recovery
	// screen are different questions.
	Sessions(archived bool) ([]SessionInfo, error)
	// OpenSession switches the kernel to a conversation already on disk.
	OpenSession(path string) error
	// NewSession abandons the current conversation and starts an empty one.
	NewSession() error
	// ForkPoints lists where the conversation in view can be branched: the end
	// of it first, then every user message in the order it was said.
	ForkPoints() []ForkPoint
	// ForkSession branches the conversation before entryID, or at the end of it
	// when entryID is empty, leaving the current one on disk untouched. The name
	// is applied to the new session; empty keeps whatever the copy inherited.
	// It returns the text of the message that was branched off — the caller
	// offers it back so the reader can say it differently.
	ForkSession(entryID, name string) (string, error)
	// ArchiveSession hides a conversation from the picker without deleting it.
	ArchiveSession(path string) error
	// RestoreSession undoes ArchiveSession.
	RestoreSession(path string) error
	// DeleteSession removes the file.
	DeleteSession(path string) error
	// Transcript replays the conversation in view. Switching to a session
	// that has a file and showing an empty page would look like the history
	// was thrown away, so the window reads it back in the same shape the live
	// stream produces.
	Transcript() ([]TranscriptBlock, error)
	// Trajectory folds the conversation in view into the rows a debugger walks.
	// It answers the question the transcript cannot: what was recorded, in what
	// order, with which arguments — including the thinking, the usage and the
	// compactions that a reading view leaves out.
	Trajectory() ([]TrajectoryRow, error)

	// Side reads the temporary conversation beside the main one. Opening and
	// closing it take a snapshot of the main history, which is a kernel
	// decision: a window that copied messages itself would be a second answer
	// to what the side conversation inherits.
	Side() (SideView, error)
	// OpenSide takes the snapshot. It is a no-op when one is already open,
	// because BeginSide discards the previous one and a page that redraws must
	// not lose the conversation it is showing.
	OpenSide() error
	// CloseSide discards it. Nothing reaches disk, so there is nothing to undo.
	CloseSide() error

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

	// Permission is the gate's posture plus the words for all four modes.
	Permission() (PermissionState, error)
	// SetPermission switches the posture. It takes effect on the next tool
	// call, because the ladder reads the mode every time it decides.
	SetPermission(mode string) error

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

// TranscriptBlock is one drawn item of a replayed conversation. It mirrors
// transcript.Block field for field, minus the bookkeeping a reader never sees,
// so the page can render a past session with the components it already has.
//
// Role is the folded role name, not a number: an integer is a stable encoding
// and a bad contract, because inserting a role in the middle of the enum would
// silently rename every one after it.
type TranscriptBlock struct {
	Role   string `json:"role"`
	Name   string `json:"name,omitempty"`
	Text   string `json:"text,omitempty"`
	Note   string `json:"note,omitempty"`
	Detail string `json:"detail,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	ID     string `json:"id,omitempty"`
}

// TrajectoryRow is one recorded fact of the conversation in view: a prompt, an
// answer, a call with its outcome, or one of the events between them.
//
// It mirrors trajectory.Row for the same reason TranscriptBlock mirrors
// transcript.Block — the transport stays ignorant of the session format — and
// the two are kept equal by a test in webhost, which is the one package allowed
// to hold both. Kind is a string for the reason Role is: the fold's set of kinds
// can grow without renaming the rows already on the wire.
//
// Nothing here is truncated. The window decides what to unfold, because the
// evidence is the point of the view and a cut in the transport would be a fact
// the page cannot recover.
type TrajectoryRow struct {
	// Index is the row's position in the fold and the only unique key: a
	// round's calls share the round's Seq.
	Index    int    `json:"index"`
	Seq      int    `json:"seq"`
	EntryID  string `json:"entryId"`
	ParentID string `json:"parentId,omitempty"`
	// Turn numbers the prompt the row belongs to, from 1. Rows recorded before
	// any prompt are 0.
	Turn      int    `json:"turn"`
	Kind      string `json:"kind"`
	Timestamp string `json:"timestamp"`
	// DurationMs is a recorded wall-clock duration: the call, or the round.
	DurationMs  int64 `json:"durationMs,omitempty"`
	IsError     bool  `json:"isError,omitempty"`
	Interrupted bool  `json:"interrupted,omitempty"`

	Tool string `json:"tool,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Text string `json:"text,omitempty"`
	// Thinking is the round's reasoning, which the transcript drops.
	Thinking string      `json:"thinking,omitempty"`
	Error    string      `json:"error,omitempty"`
	Args     string      `json:"args,omitempty"`
	Result   string      `json:"result,omitempty"`
	Model    string      `json:"model,omitempty"`
	Usage    *wire.Usage `json:"usage,omitempty"`
	// TokensBefore is how large the context was when a compaction replaced it.
	TokensBefore int64 `json:"tokensBefore,omitempty"`
}

// SideView is the temporary conversation beside the main one.
//
// Blocks covers the whole conversation the side agent holds, inherited prefix
// included: the panel draws a divider at Inherited rather than dropping the
// messages, because a reader asking the side "what did we just say" is entitled
// to see the same words the model was given. Inherited counts blocks, not
// messages, so the divider cannot land inside a tool card; it is 0 when nothing
// was inherited, and Open is false when there is no side conversation at all —
// the page then draws no panel and Blocks is empty.
type SideView struct {
	Open      bool              `json:"open"`
	Inherited int               `json:"inherited"`
	Blocks    []TranscriptBlock `json:"blocks"`
}

// ForkPoint is one place the conversation can be branched before.
//
// Index is the number the reader picks it by, and 0 is the end of the
// conversation: that point has no entry to name, which is why EntryID is empty
// there rather than absent — "the whole conversation" is a row like any other,
// and the page draws it from the same list.
//
// Text is the message in full, not a preview. The window already holds the
// whole transcript, so a cut here would save nothing and would put a second
// truncation rule next to the one the terminal uses.
type ForkPoint struct {
	EntryID string `json:"entryId,omitempty"`
	Index   int    `json:"index"`
	Text    string `json:"text,omitempty"`
}

// ModelOption is one selectable model. Levels is empty for a model that does// not reason, which is what lets the picker hide the thinking control instead
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

// PermissionMode is one selectable posture. Value is the config spelling the
// kernel accepts; Label and Note are the words the reader sees, sent from the
// same table the terminal's /permission menu uses so the two surfaces cannot
// name one posture differently.
type PermissionMode struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// PermissionState is the gate's posture plus the whole menu in one answer, for
// the same reason ModelList carries the catalog: a sheet that opens with four
// rows and no current one cannot draw its own selection.
type PermissionState struct {
	Mode  string           `json:"mode"`
	Modes []PermissionMode `json:"modes"`
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
	s.mux.HandleFunc("GET /api/sessions/transcript", s.serveSessionTranscript)
	s.mux.HandleFunc("GET /api/sessions/trajectory", s.serveSessionTrajectory)
	s.mux.HandleFunc("POST /api/sessions/open", s.serveSessionOpen)
	s.mux.HandleFunc("POST /api/sessions/new", s.serveSessionNew)
	s.mux.HandleFunc("POST /api/sessions/fork", s.serveSessionFork)
	s.mux.HandleFunc("GET /api/sessions/fork-points", s.serveSessionForkPoints)
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
	s.mux.HandleFunc("GET /api/permission", s.servePermission)
	s.mux.HandleFunc("POST /api/permission", s.servePermissionSet)
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

// serveSessionTranscript replays the conversation in view.
//
// It is a GET because it has no effect: the switch already happened through
// /api/sessions/open, and this only reads what that switch made current. A
// path parameter would invite reading a session the kernel never opened, and
// the reply would then describe a conversation that is not the one on screen.
func (s *Server) serveSessionTranscript(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	blocks, err := s.host.Transcript()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if blocks == nil {
		blocks = []TranscriptBlock{}
	}
	writeJSON(w, blocks)
}

// serveSessionTrajectory reports every recorded fact of the conversation in
// view, in file order.
//
// No path parameter, for the reason the transcript has none: the conversation
// on screen is the one the switch made current, and a route that could name any
// session would answer for a conversation the reader is not looking at. The
// fold only follows the current branch, so a fork's abandoned attempts are not
// in the answer — they are not part of the path being debugged.
func (s *Server) serveSessionTrajectory(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	rows, err := s.host.Trajectory()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if rows == nil {
		rows = []TrajectoryRow{}
	}
	writeJSON(w, rows)
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

// forkSessionRequest is the body of /api/sessions/fork. Both halves are
// optional: no entryID means the end of the conversation, and no name means the
// copy keeps the name it inherited, which is what the terminal's bare /fork
// does too.
type forkSessionRequest struct {
	EntryID string `json:"entryId"`
	Name    string `json:"name"`
}

// forkSessionResponse carries the message that was branched off, so the page can
// put it back in the input box. It is empty for a fork at the end: nothing was
// left behind to say differently.
type forkSessionResponse struct {
	OK   bool   `json:"ok"`
	Text string `json:"text,omitempty"`
}

// serveSessionForkPoints answers where the conversation in view can be branched.
//
// It names no session, for the same reason the transcript does not: the kernel
// holds one, and a path parameter would let a stale row describe another.
func (s *Server) serveSessionForkPoints(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	points := s.host.ForkPoints()
	if points == nil {
		points = []ForkPoint{}
	}
	writeJSON(w, points)
}

// serveSessionFork continues the conversation in a new session.
//
// It is a POST even though the old conversation is only read: the kernel's
// current session moves to the new one, which is the same kind of change
// /api/sessions/open makes. The body is optional, so a caller with no name to
// give sends nothing rather than an empty object.
func (s *Server) serveSessionFork(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request forkSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	text, err := s.host.ForkSession(request.EntryID, request.Name)
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishStatus(statusEvent{Running: s.running.Load()})
	writeJSON(w, forkSessionResponse{OK: true, Text: text})
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

// servePermission reports the posture in force and the four rows a sheet draws.
func (s *Server) servePermission(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	state, err := s.host.Permission()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if state.Modes == nil {
		state.Modes = []PermissionMode{}
	}
	writeJSON(w, state)
}

// servePermissionSet switches the posture.
//
// The same shape as /api/thinking: one field, and a refusal carries a status
// the sheet can show. An unknown mode is refused by the kernel rather than
// quietly stored, because a typo that silently meant "ask" would look like the
// switch did nothing.
func (s *Server) servePermissionSet(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Mode == "" {
		http.Error(w, "httpapi: empty mode", http.StatusBadRequest)
		return
	}
	if err := s.host.SetPermission(request.Mode); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	state, err := s.host.Permission()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if state.Modes == nil {
		state.Modes = []PermissionMode{}
	}
	writeJSON(w, state)
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

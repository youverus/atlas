package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

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
	// of it first, then every answer in the order it was said.
	ForkPoints() []ForkPoint
	// ForkSession branches the conversation after entryID, keeping that answer,
	// or at the end of it when entryID is empty, leaving the current one on disk
	// untouched. The name is applied to the new session; empty keeps whatever
	// the copy inherited.
	ForkSession(entryID, name string) error
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

// NamedProjectHost is an optional extension to Host that stores a custom
// display name alongside the project directory. Keeping it separate from Host
// lets partial Host stubs in transport tests keep compiling untouched.
type NamedProjectHost interface {
	AddNamedProject(path, name string) error
}

// RemoteProjectHost is an optional extension to Host for managing projects
// and sessions anchored on remote SSH compute devices.
type RemoteProjectHost interface {
	AddRemoteProject(deviceID, path, name string) error
	RemoveRemoteProject(deviceID, path string) error
	NewRemoteSessionIn(deviceID, cwd string) error
}

// BrowseDirEntry describes one subdirectory returned by ProjectBrowser.
type BrowseDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// DirBrowseResult lists subdirectories inside a local or remote directory.
type DirBrowseResult struct {
	DeviceID string           `json:"deviceId,omitempty"`
	Path     string           `json:"path"`
	Parent   string           `json:"parent,omitempty"`
	Home     string           `json:"home,omitempty"`
	Entries  []BrowseDirEntry `json:"entries"`
}

// ProjectBrowser is an optional extension to Host for browsing directories
// when adding a project without relying on the local OS file picker.
type ProjectBrowser interface {
	BrowseDirs(deviceID, path string, includeHidden bool) (DirBrowseResult, error)
}

// DiscoveredProject describes an existing project directory or workspace with
// historical sessions on a target machine.
type DiscoveredProject struct {
	DeviceID     string `json:"deviceId,omitempty"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	SessionCount int    `json:"sessionCount,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Added        bool   `json:"added,omitempty"`
}

// DiscoverProjectsResult is returned by GET /api/projects/discover.
type DiscoverProjectsResult struct {
	DeviceID string              `json:"deviceId,omitempty"`
	Projects []DiscoveredProject `json:"projects"`
}

// ProjectDiscoverer is an optional extension to Host for discovering existing
// projects and session workspaces on a local or connected remote machine.
type ProjectDiscoverer interface {
	DiscoverProjects(deviceID string) ([]DiscoveredProject, error)
}

// SessionStatus reports live properties of the conversation in view beyond the
// static startup Facts. It is separate from Host for the same reason Picker is:
// partial Host stubs in transport tests embed Host without implementing every
// optional method, and /api/state must not fail or panic against them.
type SessionStatus interface {
	SessionArchived() bool
	SessionDirectory() string
}

// LiveModelStatus is an optional extension of Host for reporting the current
// "provider/model" name in /api/state and hello frames after runtime switches or
// config reloads.
type LiveModelStatus interface {
	SessionModel() string
}

// ConfigReloader is an optional extension of Host for reloading configuration
// from disk while the server is running. Builds without one answer 501.
type ConfigReloader interface {
	ReloadConfig() error
}

// SessionTracker is an optional extension of Host that identifies the session
// currently viewed in the window for multi-session event routing.
type SessionTracker interface {
	CurrentSession() string
}

// MCPStatus is an optional extension of Host for inspecting configured MCP
// servers and refreshing their tool listings without interrupting active turns.
type MCPStatus interface {
	MCP() (MCPState, error)
	RefreshMCP(name string) (MCPState, error)
}

// SkillStatus is an optional extension of Host for inspecting installed skills
// and reading or preparing a skill prompt without interrupting active turns.
type SkillStatus interface {
	Skills() (SkillState, error)
	SkillContent(name string) (SkillDetail, error)
	LoadSkillPrompt(name, args string) (string, error)
}

// MCPSettings allows a host to change the enabled state of an MCP server.
type MCPSettings interface {
	SetMCPEnabled(ctx context.Context, name string, enabled bool) (MCPState, error)
}

// SkillSettings allows a host to change the enabled state of a skill.
type SkillSettings interface {
	SetSkillEnabled(ctx context.Context, name string, enabled bool) (SkillState, error)
}

// PluginStatus is an optional extension of Host for inspecting installed
// plugins and their contributed skills and MCP servers.
type PluginStatus interface {
	Plugins() (PluginState, error)
}

// PluginInfo describes one installed plugin and what it contributes.
type PluginInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Dir         string   `json:"dir"`
	SkillsDir   string   `json:"skillsDir,omitempty"`
	SkillNames  []string `json:"skillNames"`
	Servers     []string `json:"servers"`
}

// PluginState lists installed plugins and any manifest/scan warnings.
type PluginState struct {
	Dir     string       `json:"dir,omitempty"`
	Plugins []PluginInfo `json:"plugins"`
	Notes   []string     `json:"notes,omitempty"`
}

// BrowserStatus is an optional extension of Host for managing the controlled
// Chromium browser tabs and captures from the GUI without disturbing active turns.
type BrowserStatus interface {
	BrowserState() (BrowserState, error)
	BrowserOpen(url string) (BrowserTabInfo, error)
	BrowserNavigate(tabID, url string) (BrowserTabInfo, error)
	BrowserAction(tabID, action string) error
	BrowserScreenshot(tabID string) (BrowserCapture, error)
}

// BrowserTabInfo describes one managed Chromium browser tab.
type BrowserTabInfo struct {
	ID           string `json:"id"`
	TargetID     string `json:"targetId"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Generation   int    `json:"generation"`
	Loading      bool   `json:"loading"`
	CanGoBack    bool   `json:"canGoBack"`
	CanGoForward bool   `json:"canGoForward"`
	Control      string `json:"control"`
	Paused       bool   `json:"paused,omitempty"`
	Active       bool   `json:"active"`
	CreatedAt    int64  `json:"createdAt"`
}

// BrowserState describes the controlled Chromium availability, status, and tabs.
type BrowserState struct {
	Available  bool             `json:"available"`
	Executable string           `json:"executable,omitempty"`
	ProfileDir string           `json:"profileDir,omitempty"`
	Running    bool             `json:"running"`
	ActiveTab  string           `json:"activeTab,omitempty"`
	Tabs       []BrowserTabInfo `json:"tabs"`
}

// BrowserCapture carries one viewport screenshot of a browser tab.
type BrowserCapture struct {
	TabID  string `json:"tabId"`
	MIME   string `json:"mime"`
	Base64 string `json:"base64"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// SkillRootInfo describes one skill root directory scanned for the session.
type SkillRootInfo struct {
	Scope string `json:"scope"`
	Dir   string `json:"dir"`
}

// SkillInfo describes one discovered skill available to the session.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	Disabled    bool   `json:"disabled,omitempty"`
}

// SkillState is the full skill catalog snapshot for the control panel.
type SkillState struct {
	Cwd       string          `json:"cwd,omitempty"`
	Roots     []SkillRootInfo `json:"roots"`
	Skills    []SkillInfo     `json:"skills"`
	Notes     []string        `json:"notes,omitempty"`
	CanToggle bool            `json:"canToggle,omitempty"`
}

// SkillDetail carries one skill's metadata along with its SKILL.md content.
type SkillDetail struct {
	SkillInfo
	Body string `json:"body"`
}

// MCPToolInfo describes one remote MCP tool exposed to the session.
type MCPToolInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	WireName    string `json:"wireName"`
	Description string `json:"description,omitempty"`
}

// MCPServerInfo describes one configured MCP server and its connection state.
type MCPServerInfo struct {
	Name         string        `json:"name"`
	Version      string        `json:"version,omitempty"`
	Transport    string        `json:"transport,omitempty"`
	State        string        `json:"state"`
	Declared     int           `json:"declared"`
	Tools        []MCPToolInfo `json:"tools"`
	Missing      []string      `json:"missing,omitempty"`
	Error        string        `json:"error,omitempty"`
	RefreshError string        `json:"refreshError,omitempty"`
}

// MCPState is the full MCP server snapshot for the control panel.
type MCPState struct {
	ConfigPath string          `json:"configPath,omitempty"`
	Servers    []MCPServerInfo `json:"servers"`
	CanToggle  bool            `json:"canToggle,omitempty"`
}

// ProjectGroup is one folder in the rail: a project and its conversations.
//
// Default marks the folder a conversation starts in when the reader has not
// picked another one. It is always drawn — a rail whose only group vanished
// with the last project would leave the reader no way to start anything.
type ProjectGroup struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	DeviceID    string `json:"deviceId,omitempty"`
	DeviceName  string `json:"deviceName,omitempty"`
	DeviceState string `json:"deviceState,omitempty"`
	RemoteError string `json:"remoteError,omitempty"`
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
	DeviceID     string `json:"deviceId,omitempty"`
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
	Role         string               `json:"role"`
	Name         string               `json:"name,omitempty"`
	Text         string               `json:"text,omitempty"`
	Note         string               `json:"note,omitempty"`
	Detail       string               `json:"detail,omitempty"`
	Args         string               `json:"args,omitempty"`
	Result       string               `json:"result,omitempty"`
	ID           string               `json:"id,omitempty"`
	Timestamp    int64                `json:"timestamp,omitempty"`
	DurationMs   int64                `json:"durationMs,omitempty"`
	TokensPerSec float64              `json:"tokensPerSec,omitempty"`
	Subagent     *wire.SubagentUpdate `json:"subagent,omitempty"`
	Artifact     *wire.ArtifactRef    `json:"artifact,omitempty"`
	ErrorCode    string               `json:"errorCode,omitempty"`
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
	Running   bool              `json:"running"`
	TurnID    string            `json:"turnId,omitempty"`
}

// ForkPoint is one place the conversation can be branched from: after an
// answer.
//
// Index is the number the reader picks it by, and 0 is the end of the
// conversation: that point has no entry to name, which is why EntryID is empty
// there rather than absent — "the whole conversation" is a row like any other,
// and the page draws it from the same list.
//
// Turn is the prompt the answer closed, counted from 1. The window groups its
// transcript by prompt, so this is how a fork button drawn under a reply finds
// the entry behind it: the transcript carries no entry ids of its own.
//
// Text is the answer in full, not a preview. The window already holds the whole
// transcript, so a cut here would save nothing and would put a second
// truncation rule next to the one the terminal uses.
type ForkPoint struct {
	EntryID string `json:"entryId,omitempty"`
	Index   int    `json:"index"`
	Turn    int    `json:"turn,omitempty"`
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
// that cannot draw its own selection. Pending names a reloaded default model
// queued until the running turn finishes and the next user message starts.
type ModelList struct {
	Current  string        `json:"current"`
	Pending  string        `json:"pending,omitempty"`
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
	SessionID          string   `json:"sessionId,omitempty"`
	SessionFile        string   `json:"sessionFile,omitempty"`
	UserMessages       int      `json:"userMessages"`
	AssistantMessages  int      `json:"assistantMessages"`
	ToolCalls          int      `json:"toolCalls"`
	TotalMessages      int      `json:"totalMessages"`
	InputTokens        int64    `json:"inputTokens"`
	OutputTokens       int64    `json:"outputTokens"`
	TotalTokens        int64    `json:"totalTokens"`
	Cost               float64  `json:"cost"`
	ContextTokens      int      `json:"contextTokens"`
	ContextWindow      int      `json:"contextWindow"`
	MCPServers         int      `json:"mcpServers"`
	MCPConfigured      int      `json:"mcpConfigured"`
	MCPTools           int      `json:"mcpTools"`
	MCPTokens          int      `json:"mcpTokens"`
	Skills             int      `json:"skills"`
	SkillsTokens       int      `json:"skillsTokens"`
	AgentsMDLoaded     bool     `json:"agentsMdLoaded"`
	ContextFiles       []string `json:"contextFiles,omitempty"`
	ContextFilesTokens int      `json:"contextFilesTokens"`
	SystemPrompt       string   `json:"systemPrompt,omitempty"`
	SystemPromptTokens int      `json:"systemPromptTokens"`
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
	Root    string      `json:"root,omitempty"`
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
	s.mux.HandleFunc("GET /api/projects/browse", s.serveProjectBrowse)
	s.mux.HandleFunc("GET /api/projects/discover", s.serveProjectDiscover)
	s.mux.HandleFunc("GET /api/models", s.serveModels)
	s.mux.HandleFunc("POST /api/models", s.serveModelSet)
	s.mux.HandleFunc("POST /api/thinking", s.serveThinkingSet)
	s.mux.HandleFunc("GET /api/permission", s.servePermission)
	s.mux.HandleFunc("POST /api/permission", s.servePermissionSet)
	s.mux.HandleFunc("POST /api/config/reload", s.serveConfigReload)
	s.mux.HandleFunc("GET /api/config/models", s.serveModelConfig)
	s.mux.HandleFunc("POST /api/config/models", s.serveModelConfigSave)
	s.mux.HandleFunc("GET /api/mcp", s.serveMCP)
	s.mux.HandleFunc("POST /api/mcp/refresh", s.serveMCPRefresh)
	s.mux.HandleFunc("POST /api/mcp/enabled", s.serveMCPEnabled)
	s.mux.HandleFunc("GET /api/skills", s.serveSkills)
	s.mux.HandleFunc("GET /api/skills/content", s.serveSkillContent)
	s.mux.HandleFunc("POST /api/skills/load", s.serveSkillLoad)
	s.mux.HandleFunc("POST /api/skills/enabled", s.serveSkillEnabled)
	s.mux.HandleFunc("GET /api/plugins", s.servePlugins)
	s.mux.HandleFunc("GET /api/browser", s.serveBrowser)
	s.mux.HandleFunc("POST /api/browser/open", s.serveBrowserOpen)
	s.mux.HandleFunc("POST /api/browser/navigate", s.serveBrowserNavigate)
	s.mux.HandleFunc("POST /api/browser/action", s.serveBrowserAction)
	s.mux.HandleFunc("GET /api/browser/screenshot", s.serveBrowserScreenshot)
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
	s.publishStatus(s.currentStatusEvent())
	writeJSON(w, map[string]bool{"ok": true})
	s.WakeSubagents(s.currentSessionKey())
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
	Cwd      string `json:"cwd"`
	DeviceID string `json:"deviceId,omitempty"`
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
	deviceID := strings.TrimSpace(request.DeviceID)
	if deviceID != "" && deviceID != "local" {
		remote, ok := s.host.(RemoteProjectHost)
		if !ok {
			http.Error(w, "httpapi: remote projects are not configured", http.StatusNotImplemented)
			return
		}
		err = remote.NewRemoteSessionIn(deviceID, request.Cwd)
	} else if request.Cwd == "" {
		err = s.host.NewSession()
	} else {
		err = s.host.NewSessionIn(request.Cwd)
	}
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishStatus(s.currentStatusEvent())
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

// forkSessionResponse says the fork happened. Nothing comes back with it: a
// branch taken after a reply keeps that reply, so there is no message left
// behind to put in the input box.
type forkSessionResponse struct {
	OK bool `json:"ok"`
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
	if err := s.host.ForkSession(request.EntryID, request.Name); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishStatus(s.currentStatusEvent())
	writeJSON(w, forkSessionResponse{OK: true})
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

type projectAddRequest struct {
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
}

func (s *Server) serveProjectAdd(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request projectAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Path) == "" {
		http.Error(w, "httpapi: empty path", http.StatusBadRequest)
		return
	}
	var err error
	deviceID := strings.TrimSpace(request.DeviceID)
	if deviceID != "" && deviceID != "local" {
		remote, ok := s.host.(RemoteProjectHost)
		if !ok {
			http.Error(w, "httpapi: remote projects are not configured", http.StatusNotImplemented)
			return
		}
		err = remote.AddRemoteProject(deviceID, request.Path, request.Name)
	} else if named, ok := s.host.(NamedProjectHost); ok {
		err = named.AddNamedProject(request.Path, request.Name)
	} else {
		err = s.host.AddProject(request.Path)
	}
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type projectRemoveRequest struct {
	Path     string `json:"path"`
	DeviceID string `json:"deviceId,omitempty"`
}

func (s *Server) serveProjectRemove(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	var request projectRemoveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Path) == "" {
		http.Error(w, "httpapi: empty path", http.StatusBadRequest)
		return
	}
	var err error
	deviceID := strings.TrimSpace(request.DeviceID)
	if deviceID != "" && deviceID != "local" {
		remote, ok := s.host.(RemoteProjectHost)
		if !ok {
			http.Error(w, "httpapi: remote projects are not configured", http.StatusNotImplemented)
			return
		}
		err = remote.RemoveRemoteProject(deviceID, request.Path)
	} else {
		err = s.host.RemoveProject(request.Path)
	}
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveProjectBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	browser, ok := s.host.(ProjectBrowser)
	if !ok {
		http.Error(w, "httpapi: project directory browsing is not configured", http.StatusNotImplemented)
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device"))
	path := r.URL.Query().Get("path")
	includeHidden, _ := strconv.ParseBool(r.URL.Query().Get("hidden"))
	res, err := browser.BrowseDirs(deviceID, path, includeHidden)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if res.Entries == nil {
		res.Entries = []BrowseDirEntry{}
	}
	writeJSON(w, res)
}

func (s *Server) serveProjectDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	discoverer, ok := s.host.(ProjectDiscoverer)
	if !ok {
		http.Error(w, "httpapi: project discovery is not configured", http.StatusNotImplemented)
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device"))
	list, err := discoverer.DiscoverProjects(deviceID)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if list == nil {
		list = []DiscoveredProject{}
	}
	writeJSON(w, DiscoverProjectsResult{
		DeviceID: deviceID,
		Projects: list,
	})
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
	if session := s.currentSessionKey(); session == request.Path {
		s.WakeSubagents(session)
	}
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

func (s *Server) serveConfigReload(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	reloader, ok := s.host.(ConfigReloader)
	if !ok {
		http.Error(w, "httpapi: this build cannot reload configuration", http.StatusNotImplemented)
		return
	}
	if err := reloader.ReloadConfig(); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	provider, ok := s.host.(MCPStatus)
	if !ok {
		http.Error(w, "httpapi: this build does not expose MCP status", http.StatusNotImplemented)
		return
	}
	state, err := provider.MCP()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, normalizeMCPState(state))
}

func (s *Server) serveMCPRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	provider, ok := s.host.(MCPStatus)
	if !ok {
		http.Error(w, "httpapi: this build does not expose MCP status", http.StatusNotImplemented)
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	state, err := provider.RefreshMCP(request.Name)
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, normalizeMCPState(state))
}

func (s *Server) serveMCPEnabled(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	settings, ok := s.host.(MCPSettings)
	if !ok {
		http.Error(w, "httpapi: this build cannot change MCP settings", http.StatusNotImplemented)
		return
	}
	name, enabled, ok := readCapabilitySwitch(w, r)
	if !ok {
		return
	}
	state, err := settings.SetMCPEnabled(r.Context(), name, enabled)
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, normalizeMCPState(state))
}

func readCapabilitySwitch(w http.ResponseWriter, r *http.Request) (name string, enabled, ok bool) {
	var request struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return "", false, false
	}
	name = strings.TrimSpace(request.Name)
	if name == "" || request.Enabled == nil {
		http.Error(w, "httpapi: name and enabled are required", http.StatusBadRequest)
		return "", false, false
	}
	return name, *request.Enabled, true
}

func normalizeMCPState(state MCPState) MCPState {
	if state.Servers == nil {
		state.Servers = []MCPServerInfo{}
	}
	for index := range state.Servers {
		if state.Servers[index].Tools == nil {
			state.Servers[index].Tools = []MCPToolInfo{}
		}
	}
	return state
}

func (s *Server) needSkillStatus(w http.ResponseWriter) (SkillStatus, bool) {
	if !s.needHost(w) {
		return nil, false
	}
	provider, ok := s.host.(SkillStatus)
	if !ok {
		http.Error(w, "httpapi: this build does not expose skills", http.StatusNotImplemented)
		return nil, false
	}
	return provider, true
}

func (s *Server) serveSkills(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needSkillStatus(w)
	if !ok {
		return
	}
	state, err := provider.Skills()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, normalizeSkillState(state))
}

func (s *Server) serveSkillEnabled(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	settings, ok := s.host.(SkillSettings)
	if !ok {
		http.Error(w, "httpapi: this build cannot change Skill settings", http.StatusNotImplemented)
		return
	}
	name, enabled, ok := readCapabilitySwitch(w, r)
	if !ok {
		return
	}
	state, err := settings.SetSkillEnabled(r.Context(), name, enabled)
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, normalizeSkillState(state))
}

func normalizeSkillState(state SkillState) SkillState {
	if state.Roots == nil {
		state.Roots = []SkillRootInfo{}
	}
	if state.Skills == nil {
		state.Skills = []SkillInfo{}
	}
	return state
}

func (s *Server) serveSkillContent(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needSkillStatus(w)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "httpapi: empty skill name", http.StatusBadRequest)
		return
	}
	detail, err := provider.SkillContent(name)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, detail)
}

func (s *Server) serveSkillLoad(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needSkillStatus(w)
	if !ok {
		return
	}
	var request struct {
		Name string `json:"name"`
		Args string `json:"args"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Name == "" {
		http.Error(w, "httpapi: empty skill name", http.StatusBadRequest)
		return
	}
	prompt, err := provider.LoadSkillPrompt(request.Name, request.Args)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"prompt": prompt})
}

func (s *Server) servePlugins(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	provider, ok := s.host.(PluginStatus)
	if !ok {
		http.Error(w, "httpapi: this build does not expose plugins", http.StatusNotImplemented)
		return
	}
	state, err := provider.Plugins()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if state.Plugins == nil {
		state.Plugins = []PluginInfo{}
	}
	for i := range state.Plugins {
		if state.Plugins[i].SkillNames == nil {
			state.Plugins[i].SkillNames = []string{}
		}
		if state.Plugins[i].Servers == nil {
			state.Plugins[i].Servers = []string{}
		}
	}
	writeJSON(w, state)
}

func (s *Server) needBrowserStatus(w http.ResponseWriter) (BrowserStatus, bool) {
	if !s.needHost(w) {
		return nil, false
	}
	provider, ok := s.host.(BrowserStatus)
	if !ok {
		http.Error(w, "httpapi: this build does not expose built-in browser", http.StatusNotImplemented)
		return nil, false
	}
	return provider, true
}

func (s *Server) serveBrowser(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needBrowserStatus(w)
	if !ok {
		return
	}
	state, err := provider.BrowserState()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if state.Tabs == nil {
		state.Tabs = []BrowserTabInfo{}
	}
	writeJSON(w, state)
}

func (s *Server) serveBrowserOpen(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needBrowserStatus(w)
	if !ok {
		return
	}
	var request struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	tab, err := provider.BrowserOpen(request.URL)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, tab)
}

func (s *Server) serveBrowserNavigate(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needBrowserStatus(w)
	if !ok {
		return
	}
	var request struct {
		TabID string `json:"tabId"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	tab, err := provider.BrowserNavigate(request.TabID, request.URL)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, tab)
}

func (s *Server) serveBrowserAction(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needBrowserStatus(w)
	if !ok {
		return
	}
	var request struct {
		TabID  string `json:"tabId"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := provider.BrowserAction(request.TabID, request.Action); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	state, err := provider.BrowserState()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if state.Tabs == nil {
		state.Tabs = []BrowserTabInfo{}
	}
	writeJSON(w, state)
}

func (s *Server) serveBrowserScreenshot(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.needBrowserStatus(w)
	if !ok {
		return
	}
	capture, err := provider.BrowserScreenshot(r.URL.Query().Get("tabId"))
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, capture)
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

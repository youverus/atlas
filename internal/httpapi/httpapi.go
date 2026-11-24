// Package httpapi serves one conversation over loopback HTTP.
//
// It exists because a windowed interface needs a data plane the terminal does
// not: the TUI holds the transcript in memory and paints it, while a webview
// can only read what someone sends it. The wire chosen here is the simplest one
// that survives both a desktop shell and, later, a browser tab — HTTP for
// commands, Server-Sent Events for the stream — so the frontend has no coupling
// to any particular shell and the same page can be embedded or opened directly.
//
// Three rules follow from binding to 127.0.0.1 and owning an approval gate:
//
//   - Loopback only, and the Host header must agree. A page on the open
//     internet can POST to 127.0.0.1; DNS rebinding makes that worse because
//     the browser sends its own name as Host. Checking Host and Origin is what
//     separates "our window" from "any tab".
//   - The event stream is the only outbound channel. A turn reports by
//     publishing events, never by writing to the POST response, because the
//     response belongs to a request that already returned.
//   - Nothing here knows what a provider is. Turn is declared here and
//     implemented by the composition root, the same inversion tool/shell uses
//     for its sandbox and recorder.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"

	"pi-go/internal/httpapi/web"
	"pi-go/internal/wire"
)

// Turn runs one prompt and reports what happens while it runs.
//
// The error is reported to the reader as a status event rather than as a
// failed request: the POST that started the turn is long gone by then. An
// implementation that returns with a nil error is done; one that returns an
// error has already published whatever it managed to do.
//
// The receipt is returned rather than published as an agent event, because the
// agent protocol has no such event: what a turn cost is the host's account of
// it, and the transport only carries that account to the window.
type Turn func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error)

// Compact runs one manual compaction of the conversation in view.
type Compact func(ctx context.Context, note string, emit func(wire.AgentEvent) error) error

// Attachment carries one file uploaded from the composer (image, pdf, or text).
type Attachment struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType,omitempty"`
	Data     string `json:"data"`
}

// SessionTurn runs one prompt on a named session in multi-session hosts so
// multiple conversations can run concurrently without blocking each other.
type SessionTurn func(ctx context.Context, session, prompt string, emit func(wire.AgentEvent) error) (Receipt, error)

// SessionAttachmentTurn runs one prompt carrying attachments on a named session.
type SessionAttachmentTurn func(ctx context.Context, session, prompt string, attachments []Attachment, emit func(wire.AgentEvent) error) (Receipt, error)

// SessionCompact runs one manual compaction on a named session.
type SessionCompact func(ctx context.Context, session, note string, emit func(wire.AgentEvent) error) error

// Receipt is what one turn spent, in the words a reader reads.
//
// The parts arrive already spelled ("1723 tokens (in 1204, out 519)",
// "62 tok/s", "took 8.4s", "ended at 15:03:11") because the terminal prints the
// same words in the same order, and a window that re-derived them from raw
// numbers would be a second opinion on how a receipt reads. The separator is
// the window's business: the terminal joins with a dot, a browser can lay the
// parts out differently, and neither needs the transport to choose.
type Receipt struct {
	Fields       []string `json:"fields,omitempty"`
	EndedAt      int64    `json:"endedAt,omitempty"`
	DurationMs   int64    `json:"durationMs,omitempty"`
	TokensPerSec float64  `json:"tokensPerSec,omitempty"`
}

// Facts is what the page prints before anything has happened: the version, the
// directory it is working in and the model it will ask. All three come from the
// composition root — this package knows none of them.
type Facts struct {
	Version   string `json:"version,omitempty"`
	Directory string `json:"directory,omitempty"`
	Model     string `json:"model,omitempty"`
}

// Options is what a Server needs to be built.
type Options struct {
	// Turn is required. Without it every POST is refused rather than ignored,
	// because a server that silently does nothing looks like a broken client.
	Turn Turn

	// Facts is echoed to every new subscriber. Empty fields are omitted.
	Facts Facts

	// Host adds the routes a full window needs. Without it the server answers
	// only the single-conversation surface, which is what a test or the
	// -offline shell wants.
	Host Host

	// Picker adds the native folder chooser. Only a desktop shell has one; a
	// browser build leaves it nil and the route answers 501, which is the
	// signal the page uses to offer a path field instead.
	Picker Picker

	// Terminal manages interactive embedded PTY sessions. Only a local/desktop
	// shell has one; nil causes /api/terminal/* routes to answer 501.
	Terminal TerminalController

	// FocusShell raises the shell's main window (the desktop pet's
	// double-click). Nil causes /api/shell/focus to answer 501.
	FocusShell FocusShell

	// Pet manages the desktop pet companion window (the settings switch).
	// Nil causes /api/shell/pet routes to answer 501.
	Pet PetController

	// Compact runs a manual compaction. Nil means this build cannot compact,
	// and the route says so instead of pretending the command did nothing.
	Compact Compact

	// SideTurn runs one prompt on the side conversation. It is a second Turn
	// rather than a flag on the first because the two conversations have two
	// agents: routing by which one is "current" would put that state in the
	// transport, where a reload loses it.
	SideTurn Turn

	// SessionTurn, SessionAttachmentTurn, SessionCompact, and SessionSideTurn enable concurrent
	// multi-session execution scoped by session key when provided.
	SessionTurn           SessionTurn
	SessionAttachmentTurn SessionAttachmentTurn
	SessionCompact        SessionCompact
	SessionSideTurn       SessionTurn
	Subagents             SubagentController

	// UsageHistory supplies local usage totals for the statistics settings.
	UsageHistory UsageHistorySource

	// Remote adds optional management endpoints for embedded IM remote control
	// gateways (e.g. Feishu). Nil causes /api/remote* routes to answer 501.
	Remote RemoteController

	// Scheduler adds optional management endpoints for recurring/scheduled tasks.
	// Nil causes /api/schedules* routes to answer 501.
	Scheduler SchedulerController

	// Devices adds optional management endpoints for SSH remote devices.
	// Nil causes /api/devices* routes to answer 501.
	Devices DeviceController

	// Artifacts adds optional endpoints for inspecting and exporting
	// session-scoped artifacts. Nil causes /api/artifacts* routes to answer 501.
	Artifacts ArtifactController

	// RemoteRouter forwards session turns, approvals, compact, and side turns
	// for connected remote devices.
	RemoteRouter RemoteRouter

	// AuthToken, when non-empty, requires "Authorization: Bearer <token>" on
	// incoming HTTP requests (used by headless `atlas -serve` over SSH tunnels).
	AuthToken string

	// ExtensionToken, when non-empty, admits a second kind of caller: browser
	// extensions. Such a request must carry a chrome/moz/safari extension
	// Origin and "Authorization: Bearer <ExtensionToken>", so the page this
	// server serves stays token-free while an installed extension still has to
	// prove it was issued the credential. Extension route: GET /api/connection
	// reports the URL and token for the extension's options page.
	ExtensionToken string

	// ExtensionApprovals admits extensions a human approved through
	// POST /api/extension/pair, so the options page needs no token paste: the
	// request rings the ordinary approval card, and an approval persists the
	// extension origin for later requests. Nil keeps pairing off (a headless
	// -serve has no window to answer in; the token remains its path).
	ExtensionApprovals ExtensionApprovals
}

// Server is a loopback HTTP endpoint for one conversation.
type Server struct {
	turn                  Turn
	compact               Compact
	sideTurn              Turn
	sessionTurn           SessionTurn
	sessionAttachmentTurn SessionAttachmentTurn
	sessionCompact        SessionCompact
	sessionSideTurn       SessionTurn
	subagents             SubagentController
	usageHistory          UsageHistorySource
	turnContext           context.Context
	cancelTurns           context.CancelFunc
	facts                 Facts
	host                  Host
	picker                Picker
	terminal              TerminalController
	focusShell            FocusShell
	pet                   PetController
	remote                RemoteController
	scheduler             SchedulerController
	devices               DeviceController
	artifacts             ArtifactController
	remoteRouter          RemoteRouter
	authToken             string
	extensionToken        string
	extensionApprovals    ExtensionApprovals

	broker    *broker
	approvals *approvals
	mux       *http.ServeMux

	runsMu sync.Mutex
	runs   map[turnKey]*activeTurn

	listener net.Listener
	http     *http.Server

	mu     sync.Mutex
	closed bool
}

// New builds a server. It does not listen; see Listen.
func New(options Options) *Server {
	server := &Server{
		turn:                  options.Turn,
		compact:               options.Compact,
		sideTurn:              options.SideTurn,
		sessionTurn:           options.SessionTurn,
		sessionAttachmentTurn: options.SessionAttachmentTurn,
		sessionCompact:        options.SessionCompact,
		sessionSideTurn:       options.SessionSideTurn,
		subagents:             options.Subagents,
		usageHistory:          options.UsageHistory,
		facts:                 options.Facts,
		host:                  options.Host,
		picker:                options.Picker,
		terminal:              options.Terminal,
		focusShell:            options.FocusShell,
		pet:                   options.Pet,
		remote:                options.Remote,
		scheduler:             options.Scheduler,
		devices:               options.Devices,
		artifacts:             options.Artifacts,
		remoteRouter:          options.RemoteRouter,
		authToken:             options.AuthToken,
		extensionToken:        options.ExtensionToken,
		extensionApprovals:    options.ExtensionApprovals,
		broker:                newBroker(),
		approvals:             newApprovals(),
		runs:                  make(map[turnKey]*activeTurn),
	}
	server.turnContext, server.cancelTurns = context.WithCancel(context.Background())
	server.mux = http.NewServeMux()
	// One root pattern rather than an exact "/": the page owns its own assets
	// and its own deep links, and every /api route below is more specific, so
	// ServeMux still sends them to their handlers.
	server.mux.Handle("GET /", web.Handler())
	server.mux.HandleFunc("GET /api/state", server.serveState)
	server.mux.HandleFunc("GET /api/usage-history", server.serveUsageHistory)
	server.mux.HandleFunc("GET /api/connection", server.serveConnection)
	server.mux.HandleFunc("POST /api/extension/pair", server.serveExtensionPair)
	server.mux.HandleFunc("GET /api/events", server.serveEvents)
	server.mux.HandleFunc("POST /api/ask", server.serveAsk)
	server.mux.HandleFunc("POST /api/stop", server.serveStop)
	server.mux.HandleFunc("POST /api/subagents/stop", server.serveSubagentStop)
	server.mux.HandleFunc("POST /api/approval", server.serveApproval)
	server.mux.HandleFunc("POST /api/shell/focus", server.serveShellFocus)
	server.mux.HandleFunc("GET /api/shell/pet", server.servePetStatus)
	server.mux.HandleFunc("POST /api/shell/pet", server.servePetSet)
	server.mux.HandleFunc("POST /api/compact", server.serveCompact)
	server.mux.HandleFunc("GET /api/side", server.serveSide)
	server.mux.HandleFunc("POST /api/side", server.serveSideOpen)
	server.mux.HandleFunc("POST /api/side/ask", server.serveSideAsk)
	server.mux.HandleFunc("POST /api/side/close", server.serveSideClose)
	server.mountRemote()
	server.mountSchedules()
	server.mountTerminal()
	server.mountDevices()
	server.mountArtifacts()
	if options.Host != nil {
		server.mountHost()
	}
	return server
}

// Handler returns the routes without a listener, for tests and for embedding
// this in a shell that owns its own server.
func (s *Server) Handler() http.Handler {
	return s.guard(s.mux)
}

// Listen binds 127.0.0.1 on an ephemeral port and serves in the background.
// It returns the address the window should open, because a fixed port is a
// port someone else already has.
func (s *Server) Listen() (string, error) {
	return s.ListenOn("127.0.0.1:0")
}

// ListenOn binds a loopback address (defaulting to 127.0.0.1:0 when empty)
// and serves in the background. Non-loopback bind addresses are rejected so the
// server never exposes an unencrypted agent API on LAN/public interfaces.
func (s *Server) ListenOn(addr string) (string, error) {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if !loopbackName(host) {
		return "", errors.New("httpapi: listen address must be a loopback host")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		listener.Close()
		return "", errors.New("httpapi: server is closed")
	}
	s.listener = listener
	// A plain http.Server, not ListenAndServe: the address is needed before the
	// window opens, and the error of Serve must not be swallowed in a goroutine
	// nobody reads. Closing comes through Close.
	s.http = &http.Server{Handler: s.Handler()}
	s.mu.Unlock()

	go func() {
		// Serve returns ErrServerClosed on Close, which is the normal path.
		_ = s.http.Serve(listener)
	}()
	return listener.Addr().String(), nil
}

// Addr reports the bound address, or "" before Listen.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// URL is the address as the window should read it.
func (s *Server) URL() string {
	addr := s.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr
}

// Close stops the listener and drops every subscriber. It is safe to call
// twice and safe to call before Listen.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	server := s.http
	s.mu.Unlock()

	s.cancelTurns()
	s.broker.close()
	// A question still open means a turn parked on the tool goroutine. Denying
	// it here is what lets that turn finish instead of outliving the window.
	s.approvals.close()
	if server == nil {
		return nil
	}
	return server.Close()
}

// guard rejects requests that did not come from the page this server served.
//
// The Host check is the one that matters: a browser always sends the name it
// used, so a rebound name arrives here and is refused. Origin is checked when
// present for the same reason at a second layer — a cross-site POST carries the
// other site's origin. The cross-origin callers admitted are browser
// extensions: one holding the extension token (extensionAdmitted) or one the
// reader approved through the pairing route (ExtensionApprovals). That route
// itself stays reachable for a not-yet-approved extension — it is the knock on
// the door, answered by the ordinary approval card.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if !loopbackName(host) {
			http.Error(w, "httpapi: unexpected host", http.StatusForbidden)
			return
		}
		extensionReq := false
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == extensionPairPath && extensionOrigin(origin):
				// The knock on the door: pass through to the route, which
				// rings the approval card and enforces the rest.
			case s.extensionAdmitted(origin, r):
				// Admitted on the token already; the global AuthToken below is
				// for everyone else.
				extensionReq = true
			case s.extensionApprovals != nil && s.extensionApprovals.Approved(origin):
				extensionReq = true
			default:
				http.Error(w, "httpapi: unexpected origin", http.StatusForbidden)
				return
			}
		}
		if !extensionReq && s.authToken != "" {
			const prefix = "Bearer "
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, prefix) ||
				subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(s.authToken)) != 1 {
				http.Error(w, "httpapi: unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// PublishEvent pushes a pre-encoded SSE event frame onto all connected stream
// subscribers. Used by the remote SSE bridge to relay events from forwarded
// remote `atlas -serve` hosts into the local page stream.
func (s *Server) PublishEvent(name string, data []byte) {
	if s == nil || s.broker == nil || name == "" {
		return
	}
	s.broker.publish(streamEvent{Name: name, Data: data})
}

// hostOnly drops the port from a Host header. IPv6 literals keep their
// brackets; only the last colon-separated piece is a port.
func hostOnly(host string) string {
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end >= 0 {
			return host[:end+1]
		}
		return host
	}
	if colon := strings.LastIndex(host, ":"); colon >= 0 {
		return host[:colon]
	}
	return host
}

// loopbackName reports whether a host names this machine. The server already
// binds 127.0.0.1, so this is not about reachability: it is about the name a
// rebound request would carry.
func loopbackName(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "[::1]", "":
		return true
	}
	return false
}

func (s *Server) currentFacts() (Facts, bool) {
	facts := s.facts
	archived := false
	if status, ok := s.host.(SessionStatus); ok {
		archived = status.SessionArchived()
		if dir := status.SessionDirectory(); dir != "" {
			facts.Directory = dir
		}
	}
	if modelStatus, ok := s.host.(LiveModelStatus); ok {
		if model := modelStatus.SessionModel(); model != "" {
			facts.Model = model
		}
	}
	return facts, archived
}

func (s *Server) currentSessionKey() string {
	if tracker, ok := s.host.(SessionTracker); ok {
		return tracker.CurrentSession()
	}
	return ""
}

type turnKey struct {
	session string
	side    bool
}

type activeTurn struct {
	key    turnKey
	id     string
	ctx    context.Context
	cancel context.CancelCauseFunc
}

var errTurnStopped = errors.New("httpapi: turn stopped by user")

func (s *Server) turnKey(session string, side bool) turnKey {
	scoped := s.sessionTurn != nil || s.sessionAttachmentTurn != nil || s.sessionCompact != nil || s.subagents != nil
	if side {
		scoped = s.sessionSideTurn != nil
	}
	if !scoped {
		session = ""
	}
	return turnKey{session: session, side: side}
}

func (s *Server) startTurn(key turnKey) *activeTurn {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if s.turnContext.Err() != nil || s.runs[key] != nil {
		return nil
	}
	ctx, cancel := context.WithCancelCause(s.turnContext)
	run := &activeTurn{key: key, id: rand.Text(), ctx: ctx, cancel: cancel}
	s.runs[key] = run
	return run
}

func (s *Server) turnStatus(session string, side bool) statusEvent {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	status := statusEvent{Session: session}
	if run := s.runs[s.turnKey(session, side)]; run != nil {
		status.Running = true
		status.TurnID = run.id
	}
	return status
}

func (s *Server) isSessionRunning(session string) bool {
	return s.turnStatus(session, false).Running
}

func (s *Server) isSideRunning(session string) bool {
	return s.turnStatus(session, true).Running
}

func (s *Server) runningSessionList() []string {
	s.runsMu.Lock()
	out := make([]string, 0, len(s.runs))
	for key := range s.runs {
		if !key.side && key.session != "" {
			out = append(out, key.session)
		}
	}
	s.runsMu.Unlock()
	sort.Strings(out)
	return out
}

func (s *Server) currentStatusEvent() statusEvent {
	status := s.turnStatus(s.currentSessionKey(), false)
	if s.remoteRouter != nil {
		if remote, ok := s.remoteRouter.(remoteTurnStopper); ok && status.TurnID == "" {
			status.TurnID = remote.RemoteTurnID(status.Session)
		}
		for _, session := range s.remoteRouter.RunningRemoteSessions() {
			if session == status.Session {
				status.Running = true
				break
			}
		}
	}
	return status
}

// serveState answers what a page cannot know after a reload: whether a turn is
// already running, which facts belong to this server, and which approval boxes
// should be on screen. The last one is why a reload mid-approval is survivable:
// the question is still open on the tool goroutine, and only the id was lost.
func (s *Server) serveState(w http.ResponseWriter, r *http.Request) {
	pendingQuestions := s.approvals.list()
	waiting := make([]PendingQuestionItem, 0, len(pendingQuestions))
	for _, item := range pendingQuestions {
		waiting = append(waiting, PendingQuestionItem{ID: item.id, Question: item.Question})
	}
	facts, archived := s.currentFacts()
	curSession := s.currentSessionKey()
	status := s.turnStatus(curSession, false)
	running := status.Running
	runningSessions := s.runningSessionList()
	activeDeviceID := ""
	if s.remoteRouter != nil {
		for _, remoteSess := range s.remoteRouter.RunningRemoteSessions() {
			if remoteSess == curSession {
				running = true
				if remote, ok := s.remoteRouter.(remoteTurnStopper); ok {
					status.TurnID = remote.RemoteTurnID(curSession)
				}
			}
			runningSessions = append(runningSessions, remoteSess)
		}
		sort.Strings(runningSessions)
		if devID, remoteDir, ok := s.remoteRouter.ActiveRemoteDevice(); ok {
			activeDeviceID = devID
			if remoteDir != "" {
				facts.Directory = remoteDir
			}
		}
		if remotePending := s.remoteRouter.PendingRemoteApprovals(); len(remotePending) > 0 {
			waiting = append(waiting, remotePending...)
		}
	}
	state := struct {
		Facts
		DeviceID        string                `json:"deviceId,omitempty"`
		Session         string                `json:"session,omitempty"`
		Running         bool                  `json:"running"`
		TurnID          string                `json:"turnId,omitempty"`
		RunningSessions []string              `json:"runningSessions,omitempty"`
		Archived        bool                  `json:"archived,omitempty"`
		Pending         []PendingQuestionItem `json:"pending,omitempty"`
	}{
		Facts:           facts,
		DeviceID:        activeDeviceID,
		Session:         curSession,
		Running:         running,
		TurnID:          status.TurnID,
		RunningSessions: runningSessions,
		Archived:        archived,
		Pending:         waiting,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(state)
}

type askRequest struct {
	Session     string       `json:"session"`
	Prompt      string       `json:"prompt"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// serveAsk starts one turn and returns at once. The events arrive on the stream
// because the reader is already there; holding the POST open would only give
// the window a second, differently-shaped channel to read.
func (s *Server) serveAsk(w http.ResponseWriter, r *http.Request) {
	if s.turn == nil && s.sessionTurn == nil && s.remoteRouter == nil {
		http.Error(w, "httpapi: no turn configured", http.StatusNotImplemented)
		return
	}
	var request askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 28<<20)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Prompt) == "" && len(request.Attachments) == 0 {
		http.Error(w, "httpapi: empty prompt", http.StatusBadRequest)
		return
	}
	if len(request.Attachments) > 8 {
		http.Error(w, "httpapi: too many attachments (max 8)", http.StatusBadRequest)
		return
	}
	sessionKey := request.Session
	if sessionKey == "" {
		sessionKey = s.currentSessionKey()
	}
	if s.remoteRouter != nil {
		handled, err := s.remoteRouter.ForwardAsk(r.Context(), sessionKey, request.Prompt, request.Attachments)
		if handled {
			if err != nil {
				fail(w, err, http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	action := s.turn
	switch {
	case len(request.Attachments) > 0:
		if s.sessionAttachmentTurn == nil {
			http.Error(w, "httpapi: attachments are not supported in this mode", http.StatusNotImplemented)
			return
		}
		action = func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
			return s.sessionAttachmentTurn(ctx, sessionKey, prompt, request.Attachments, emit)
		}
	case s.sessionTurn != nil:
		action = func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
			return s.sessionTurn(ctx, sessionKey, prompt, emit)
		}
	}
	if action == nil {
		http.Error(w, "httpapi: no turn configured", http.StatusNotImplemented)
		return
	}
	run := s.startTurn(s.turnKey(sessionKey, false))
	if run == nil {
		http.Error(w, "httpapi: a turn is already running", http.StatusConflict)
		return
	}
	go s.runTurn(run, request.Prompt, action)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) runTurn(run *activeTurn, prompt string, action Turn) {
	s.publishTurnStatus(run.key.side, statusEvent{Session: run.key.session, TurnID: run.id, Running: true})
	emit := s.emitForSession
	if run.key.side {
		emit = s.emitSideForSession
	}
	receipt, err := action(run.ctx, prompt, func(event wire.AgentEvent) error {
		return emit(run.key.session, event)
	})
	if !s.finishTurn(run, &receipt, err) && !run.key.side && err == nil {
		s.WakeSubagents(run.key.session)
	}
}

func (s *Server) finishTurn(run *activeTurn, receipt *Receipt, err error) bool {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	aborted := errors.Is(context.Cause(run.ctx), errTurnStopped)
	run.cancel(nil)
	delete(s.runs, run.key)
	// Publish before another request can acquire the slot and announce its start.
	if receipt != nil {
		done := statusEvent{
			Session:      run.key.session,
			TurnID:       run.id,
			Aborted:      aborted,
			Receipt:      receipt.Fields,
			EndedAt:      receipt.EndedAt,
			DurationMs:   receipt.DurationMs,
			TokensPerSec: receipt.TokensPerSec,
		}
		if err != nil && !aborted {
			done.Error = err.Error()
		}
		s.publishTurnStatus(run.key.side, done)
	}
	return aborted
}

func (s *Server) publishTurnStatus(side bool, status statusEvent) {
	if side {
		s.publishSideStatus(sideStatus{Open: true, statusEvent: status})
		return
	}
	s.publishStatus(status)
}

func (s *Server) serveStop(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Session string `json:"session"`
		TurnID  string `json:"turnId"`
		Side    bool   `json:"side"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&request); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if request.TurnID == "" {
		http.Error(w, "httpapi: turn id is required", http.StatusBadRequest)
		return
	}
	if request.Session == "" {
		request.Session = s.currentSessionKey()
	}
	if remote, ok := s.remoteRouter.(remoteTurnStopper); ok {
		handled, status, err := remote.ForwardStop(r.Context(), request.Session, request.TurnID, request.Side)
		if handled {
			if err != nil {
				if status < 400 || status > 599 {
					status = http.StatusBadGateway
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	key := s.turnKey(request.Session, request.Side)
	s.runsMu.Lock()
	run := s.runs[key]
	matched := run != nil && run.id == request.TurnID
	if matched {
		run.cancel(errTurnStopped)
	}
	s.runsMu.Unlock()
	if !matched {
		http.Error(w, "httpapi: this turn is no longer active", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type scopedAgentEvent struct {
	wire.AgentEvent
	Session string `json:"session,omitempty"`
}

func (s *Server) emitForSession(session string, event wire.AgentEvent) error {
	payload, err := json.Marshal(scopedAgentEvent{AgentEvent: event, Session: session})
	if err != nil {
		return err
	}
	s.broker.publish(streamEvent{Name: "agent", Data: payload})
	return nil
}

type statusEvent struct {
	Session      string   `json:"session,omitempty"`
	TurnID       string   `json:"turnId,omitempty"`
	Running      bool     `json:"running"`
	Aborted      bool     `json:"aborted,omitempty"`
	Error        string   `json:"error,omitempty"`
	Receipt      []string `json:"receipt,omitempty"`
	EndedAt      int64    `json:"endedAt,omitempty"`
	DurationMs   int64    `json:"durationMs,omitempty"`
	TokensPerSec float64  `json:"tokensPerSec,omitempty"`
}

func (s *Server) publishStatus(status statusEvent) {
	payload, err := json.Marshal(status)
	if err != nil {
		return
	}
	s.broker.publish(streamEvent{Name: "status", Data: payload})
}

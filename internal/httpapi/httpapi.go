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
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"pi-go/internal/wire"
)

// Turn runs one prompt and reports what happens while it runs.
//
// The error is reported to the reader as a status event rather than as a
// failed request: the POST that started the turn is long gone by then. An
// implementation that returns with a nil error is done; one that returns an
// error has already published whatever it managed to do.
type Turn func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) error

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
}

// Server is a loopback HTTP endpoint for one conversation.
type Server struct {
	turn  Turn
	facts Facts

	broker *broker
	mux    *http.ServeMux

	// running is the single-turn latch. One conversation has one turn in
	// flight; a second POST while one runs is refused instead of queued,
	// because the window can be clicked twice faster than a turn finishes and
	// a queue would replay the same prompt later with no way to see it coming.
	running atomic.Bool

	listener net.Listener
	http     *http.Server

	mu     sync.Mutex
	closed bool
}

// New builds a server. It does not listen; see Listen.
func New(options Options) *Server {
	server := &Server{
		turn:   options.Turn,
		facts:  options.Facts,
		broker: newBroker(),
	}
	server.mux = http.NewServeMux()
	server.mux.HandleFunc("GET /{$}", server.servePage)
	server.mux.HandleFunc("GET /api/state", server.serveState)
	server.mux.HandleFunc("GET /api/events", server.serveEvents)
	server.mux.HandleFunc("POST /api/ask", server.serveAsk)
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
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

	s.broker.close()
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
// other site's origin.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if !loopbackName(host) {
			http.Error(w, "httpapi: unexpected host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			allowed := "http://" + r.Host
			if origin != allowed {
				http.Error(w, "httpapi: unexpected origin", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
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

// serveState answers what a page cannot know after a reload: whether a turn is
// already running, and which facts belong to this server.
func (s *Server) serveState(w http.ResponseWriter, r *http.Request) {
	state := struct {
		Facts
		Running bool `json:"running"`
	}{Facts: s.facts, Running: s.running.Load()}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(state)
}

// askRequest is the POST body. One field: a prompt is still the only thing a
// turn needs, and adding "attachments" here before the transport is proven
// would be guessing at a shape.
type askRequest struct {
	Prompt string `json:"prompt"`
}

// serveAsk starts one turn and returns at once. The events arrive on the stream
// because the reader is already there; holding the POST open would only give
// the window a second, differently-shaped channel to read.
func (s *Server) serveAsk(w http.ResponseWriter, r *http.Request) {
	if s.turn == nil {
		http.Error(w, "httpapi: no turn configured", http.StatusNotImplemented)
		return
	}
	var request askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Prompt) == "" {
		http.Error(w, "httpapi: empty prompt", http.StatusBadRequest)
		return
	}
	// Compare-and-swap rather than a lock: the loser of a double click should
	// be told, not serialized behind the winner.
	if !s.running.CompareAndSwap(false, true) {
		http.Error(w, "httpapi: a turn is already running", http.StatusConflict)
		return
	}

	prompt := request.Prompt
	go s.runTurn(prompt)
	w.WriteHeader(http.StatusAccepted)
}

// runTurn drives the injected turn and brackets it with status events, so the
// page learns the end of a turn even when the turn itself reports nothing.
func (s *Server) runTurn(prompt string) {
	defer s.running.Store(false)

	s.publishStatus(statusEvent{Running: true})
	err := s.turn(context.Background(), prompt, s.emit)
	done := statusEvent{Running: false}
	if err != nil {
		done.Error = err.Error()
	}
	s.publishStatus(done)
}

// emit publishes one agent event. The signature is the one Turn wants, so the
// composition root can hand app.Ask's emit straight through.
func (s *Server) emit(event wire.AgentEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	s.broker.publish(streamEvent{Name: "agent", Data: payload})
	return nil
}

// statusEvent is the turn-level signal: started, finished, or failed. It is a
// separate event name rather than a synthetic agent event, because "the turn
// ended" is not something the agent protocol says — a reader that only knows
// wire.AgentEvent would have to infer it from agent_end, which a cancelled or
// failed turn never sends.
type statusEvent struct {
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) publishStatus(status statusEvent) {
	payload, err := json.Marshal(status)
	if err != nil {
		return
	}
	s.broker.publish(streamEvent{Name: "status", Data: payload})
}

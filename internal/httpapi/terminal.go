package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TerminalSessionInfo describes one running interactive PTY session.
type TerminalSessionInfo struct {
	ID    string `json:"id"`
	Shell string `json:"shell,omitempty"`
	CWD   string `json:"cwd,omitempty"`
}

// TerminalEvent carries one chunk of PTY output or an exit notification.
type TerminalEvent struct {
	Type     string
	Data     []byte
	ExitCode int
}

// TerminalSubscription streams buffered backlog and live PTY events.
type TerminalSubscription struct {
	Backlog  [][]byte
	Events   <-chan TerminalEvent
	Cancel   func()
	Exited   bool
	ExitCode int
}

// TerminalController manages interactive PTY sessions embedded in the desktop window.
//
// It is separate from Host for the same reason Picker is: only a local/desktop
// host can allocate PTYs and spawn local shells; builds without one leave it nil
// and /api/terminal/* routes answer 501.
type TerminalController interface {
	CreateTerminal(dir string, cols, rows int) (TerminalSessionInfo, error)
	WriteTerminal(id string, data []byte) error
	ResizeTerminal(id string, cols, rows int) error
	CloseTerminal(id string) error
	SubscribeTerminal(id string) (TerminalSubscription, error)
}

// RemoteTerminalController is an optional extension to TerminalController that
// opens an interactive SSH shell in the remote project's directory.
type RemoteTerminalController interface {
	CreateRemoteTerminal(deviceID, dir string, cols, rows int) (TerminalSessionInfo, error)
}

func (s *Server) mountTerminal() {
	s.mux.HandleFunc("POST /api/terminal/create", s.serveTerminalCreate)
	s.mux.HandleFunc("GET /api/terminal/stream", s.serveTerminalStream)
	s.mux.HandleFunc("POST /api/terminal/write", s.serveTerminalWrite)
	s.mux.HandleFunc("POST /api/terminal/resize", s.serveTerminalResize)
	s.mux.HandleFunc("POST /api/terminal/close", s.serveTerminalClose)
}

func (s *Server) needTerminal(w http.ResponseWriter) bool {
	if s.terminal != nil {
		return true
	}
	http.Error(w, "httpapi: this build has no embedded terminal controller", http.StatusNotImplemented)
	return false
}

func (s *Server) serveTerminalCreate(w http.ResponseWriter, r *http.Request) {
	if !s.needTerminal(w) {
		return
	}
	var req struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Cols <= 0 {
		req.Cols = 80
	}
	if req.Rows <= 0 {
		req.Rows = 24
	}
	if s.remoteRouter != nil {
		if deviceID, remoteDir, ok := s.remoteRouter.ActiveRemoteDevice(); ok {
			rt, supported := s.terminal.(RemoteTerminalController)
			if !supported {
				http.Error(w, "httpapi: remote terminal is not supported", http.StatusNotImplemented)
				return
			}
			info, err := rt.CreateRemoteTerminal(deviceID, remoteDir, req.Cols, req.Rows)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			writeJSON(w, info)
			return
		}
	}
	facts, _ := s.currentFacts()
	dir := strings.TrimSpace(facts.Directory)
	if dir == "" {
		http.Error(w, "httpapi: empty session directory", http.StatusBadRequest)
		return
	}
	info, err := s.terminal.CreateTerminal(dir, req.Cols, req.Rows)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}

type terminalWireEvent struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
}

func writeTerminalSSE(w http.ResponseWriter, flusher http.Flusher, ev TerminalEvent) error {
	wireEv := terminalWireEvent{
		Type:     ev.Type,
		ExitCode: ev.ExitCode,
	}
	if ev.Type == "data" && len(ev.Data) > 0 {
		wireEv.Data = base64.StdEncoding.EncodeToString(ev.Data)
	}
	payload, err := json.Marshal(wireEv)
	if err != nil {
		return err
	}
	if _, err := w.Write(streamEvent{Data: payload}.frame()); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) serveTerminalStream(w http.ResponseWriter, r *http.Request) {
	if !s.needTerminal(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "httpapi: empty terminal id", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "httpapi: streaming unsupported", http.StatusInternalServerError)
		return
	}
	sub, err := s.terminal.SubscribeTerminal(id)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if sub.Cancel != nil {
		defer sub.Cancel()
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	for _, chunk := range sub.Backlog {
		if len(chunk) == 0 {
			continue
		}
		if err := writeTerminalSSE(w, flusher, TerminalEvent{Type: "data", Data: chunk}); err != nil {
			return
		}
	}
	if sub.Exited {
		_ = writeTerminalSSE(w, flusher, TerminalEvent{Type: "exit", ExitCode: sub.ExitCode})
		return
	}

	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-sub.Events:
			if !open {
				return
			}
			if err := writeTerminalSSE(w, flusher, ev); err != nil {
				return
			}
			if ev.Type == "exit" {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) serveTerminalWrite(w http.ResponseWriter, r *http.Request) {
	if !s.needTerminal(w) {
		return
	}
	var req struct {
		ID       string `json:"id"`
		Data     string `json:"data"`
		Encoding string `json:"encoding,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "httpapi: empty terminal id", http.StatusBadRequest)
		return
	}
	var data []byte
	if req.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			http.Error(w, "httpapi: invalid base64 data: "+err.Error(), http.StatusBadRequest)
			return
		}
		data = decoded
	} else {
		data = []byte(req.Data)
	}
	if err := s.terminal.WriteTerminal(req.ID, data); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveTerminalResize(w http.ResponseWriter, r *http.Request) {
	if !s.needTerminal(w) {
		return
	}
	var req struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "httpapi: empty terminal id", http.StatusBadRequest)
		return
	}
	if req.Cols <= 0 || req.Rows <= 0 {
		http.Error(w, "httpapi: invalid terminal size", http.StatusBadRequest)
		return
	}
	if err := s.terminal.ResizeTerminal(req.ID, req.Cols, req.Rows); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveTerminalClose(w http.ResponseWriter, r *http.Request) {
	if !s.needTerminal(w) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "httpapi: empty terminal id", http.StatusBadRequest)
		return
	}
	if err := s.terminal.CloseTerminal(req.ID); err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

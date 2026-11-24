package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"pi-go/internal/wire"
)

// The two actions the terminal has and the window did not: /compact and the
// side conversation. Both are their own routes rather than arguments on
// /api/ask, because neither is a prompt: one rewrites the context the next turn
// will read, and the other talks to a second agent that holds its own copy of
// the history.

// compactRequest is the POST body. The note is what the reader adds to steer
// the summary, and it is optional: "/compact" alone is the common case.
type compactRequest struct {
	Session string `json:"session"`
	Note    string `json:"note"`
}

func (s *Server) serveCompact(w http.ResponseWriter, r *http.Request) {
	if s.compact == nil && s.sessionCompact == nil && s.remoteRouter == nil {
		http.Error(w, "httpapi: no compact configured", http.StatusNotImplemented)
		return
	}
	var request compactRequest
	// An empty body is a compaction with no note, not a malformed request.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.remoteRouter != nil {
		sessionKey := request.Session
		if sessionKey == "" {
			sessionKey = s.currentSessionKey()
		}
		handled, ok, err := s.remoteRouter.ForwardCompact(r.Context(), sessionKey, request.Note)
		if handled {
			if err != nil {
				fail(w, err, http.StatusConflict)
				return
			}
			writeJSON(w, map[string]bool{"ok": ok})
			return
		}
	}
	if s.compact == nil && s.sessionCompact == nil {
		http.Error(w, "httpapi: no compact configured", http.StatusNotImplemented)
		return
	}
	sessionKey := request.Session
	if sessionKey == "" {
		sessionKey = s.currentSessionKey()
	}
	run := s.startTurn(s.turnKey(sessionKey, false))
	if run == nil {
		http.Error(w, "httpapi: a turn is already running", http.StatusConflict)
		return
	}
	var compactErr error
	s.runTurn(run, request.Note, func(ctx context.Context, note string, emit func(wire.AgentEvent) error) (Receipt, error) {
		if s.sessionCompact != nil {
			compactErr = s.sessionCompact(ctx, sessionKey, note, emit)
		} else {
			compactErr = s.compact(ctx, note, emit)
		}
		return Receipt{}, nil
	})
	writeJSON(w, map[string]bool{"ok": compactErr == nil})
}

type sideStatus struct {
	statusEvent
	Open bool `json:"open"`
}

func (s *Server) sideViewStatus(view SideView, status statusEvent) SideView {
	view.Running = view.Running || status.Running
	if status.TurnID != "" {
		view.TurnID = status.TurnID
	}
	return view
}

func (s *Server) serveSide(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	view, err := s.host.Side()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if view.Blocks == nil {
		view.Blocks = []TranscriptBlock{}
	}
	sessionKey := s.currentSessionKey()
	status := s.turnStatus(sessionKey, true)
	if s.remoteRouter != nil && s.remoteRouter.IsRemoteSideRunning(sessionKey) {
		status.Running = true
	}
	writeJSON(w, s.sideViewStatus(view, status))
}

// serveSideOpen takes the snapshot, or reports the one that is already open.
func (s *Server) serveSideOpen(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	sessionKey := s.currentSessionKey()
	status := s.turnStatus(sessionKey, true)
	if s.remoteRouter != nil && s.remoteRouter.IsRemoteSideRunning(sessionKey) {
		status.Running = true
	}
	if view, err := s.host.Side(); err == nil && view.Open {
		writeJSON(w, s.sideViewStatus(view, status))
		return
	}
	if err := s.host.OpenSide(); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	view, err := s.host.Side()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if view.Blocks == nil {
		view.Blocks = []TranscriptBlock{}
	}
	s.publishSideStatus(sideStatus{Open: true, statusEvent: status})
	writeJSON(w, s.sideViewStatus(view, status))
}

func (s *Server) serveSideAsk(w http.ResponseWriter, r *http.Request) {
	if s.sideTurn == nil && s.sessionSideTurn == nil && s.remoteRouter == nil {
		http.Error(w, "httpapi: no side turn configured", http.StatusNotImplemented)
		return
	}
	if !s.needHost(w) {
		return
	}
	view, err := s.host.Side()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if !view.Open {
		http.Error(w, "httpapi: no side conversation is open", http.StatusConflict)
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
	sessionKey := request.Session
	if sessionKey == "" {
		sessionKey = s.currentSessionKey()
	}
	if s.remoteRouter != nil {
		handled, err := s.remoteRouter.ForwardSideAsk(r.Context(), sessionKey, request.Prompt)
		if handled {
			if err != nil {
				fail(w, err, http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	action := s.sideTurn
	if s.sessionSideTurn != nil {
		action = func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
			return s.sessionSideTurn(ctx, sessionKey, prompt, emit)
		}
	}
	if action == nil {
		http.Error(w, "httpapi: no side turn configured", http.StatusNotImplemented)
		return
	}
	run := s.startTurn(s.turnKey(sessionKey, true))
	if run == nil {
		http.Error(w, "httpapi: a side turn is already running", http.StatusConflict)
		return
	}
	go s.runTurn(run, request.Prompt, action)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) serveSideClose(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	sessionKey := s.currentSessionKey()
	if s.isSideRunning(sessionKey) || (s.remoteRouter != nil && s.remoteRouter.IsRemoteSideRunning(sessionKey)) {
		http.Error(w, "httpapi: a side turn is running", http.StatusConflict)
		return
	}
	if err := s.host.CloseSide(); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishSideStatus(sideStatus{statusEvent: statusEvent{Session: sessionKey}})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) emitSideForSession(session string, event wire.AgentEvent) error {
	payload, err := json.Marshal(scopedAgentEvent{AgentEvent: event, Session: session})
	if err != nil {
		return err
	}
	s.broker.publish(streamEvent{Name: "sideagent", Data: payload})
	return nil
}

func (s *Server) publishSideStatus(status sideStatus) {
	payload, err := json.Marshal(status)
	if err != nil {
		return
	}
	s.broker.publish(streamEvent{Name: "sidestatus", Data: payload})
}

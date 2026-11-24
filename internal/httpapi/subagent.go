package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"pi-go/internal/wire"
)

// SubagentController links session-scoped background tasks to the host's turns.
type SubagentController interface {
	SubagentResultsPending(session string) bool
	RunSubagentResults(ctx context.Context, session string, emit func(wire.AgentEvent) error) (Receipt, error)
	StopSubagent(session, id string) error
}

// PublishSubagent publishes one task snapshot and checks for an idle result turn.
func (s *Server) PublishSubagent(update wire.SubagentUpdate) {
	if s.subagents == nil || update.SessionFile == "" {
		return
	}
	session := update.SessionFile
	update.SessionFile = ""
	_ = s.emitForSession(session, wire.AgentEvent{
		Type: wire.AgentEventSubagentUpdate, ToolCallID: update.ToolCallID, ToolName: wire.SubagentToolName, Subagent: &update,
	})
	if update.State != "queued" && update.State != "running" {
		go s.WakeSubagents(session)
	}
}

// WakeSubagents admits an internal result turn through the same gate as /api/ask.
func (s *Server) WakeSubagents(session string) {
	if s.subagents == nil || session == "" {
		return
	}
	if !s.subagents.SubagentResultsPending(session) {
		return
	}
	run := s.startTurn(turnKey{session: session})
	if run == nil {
		return
	}
	go func() {
		if s.turnContext.Err() != nil || !s.subagents.SubagentResultsPending(session) {
			s.finishTurn(run, nil, nil)
			return
		}
		s.runTurn(run, session, s.subagents.RunSubagentResults)
	}()
}

func (s *Server) serveSubagentStop(w http.ResponseWriter, r *http.Request) {
	if s.subagents == nil {
		http.Error(w, "httpapi: subagents are unavailable", http.StatusNotImplemented)
		return
	}
	var request struct {
		Session string `json:"session"`
		ID      string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&request); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.ID) == "" {
		http.Error(w, "httpapi: task id is required", http.StatusBadRequest)
		return
	}
	if request.Session == "" {
		request.Session = s.currentSessionKey()
	}
	if err := s.subagents.StopSubagent(request.Session, request.ID); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

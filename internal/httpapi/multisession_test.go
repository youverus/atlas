package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"pi-go/internal/wire"
)

type multiSessionHost struct {
	Host
	mu      sync.Mutex
	current string
}

func (h *multiSessionHost) CurrentSession() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

func (h *multiSessionHost) OpenSession(path string) error {
	h.mu.Lock()
	h.current = path
	h.mu.Unlock()
	return nil
}

func TestConcurrentSessionsRunAndScopeEvents(t *testing.T) {
	host := &multiSessionHost{current: "/sessions/a.jsonl"}
	startedA := make(chan struct{})
	startedB := make(chan struct{})
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})

	server := New(Options{
		Host: host,
		SessionTurn: func(ctx context.Context, session, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
			switch session {
			case "/sessions/a.jsonl":
				close(startedA)
				<-releaseA
			case "/sessions/b.jsonl":
				close(startedB)
				<-releaseB
			}
			_ = emit(wire.AgentEvent{Type: wire.AgentEventMessageEnd})
			return Receipt{Fields: []string{"10 tokens"}}, nil
		},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer server.Close()

	_, reader, cancel := openStream(t, server)
	defer cancel()
	readFrame(t, reader) // initial hello

	// Start session A turn.
	post(t, httpServer, "/api/ask", `{"session":"/sessions/a.jsonl","prompt":"hello A"}`, http.StatusAccepted)
	<-startedA

	// Duplicate ask on session A while running is refused.
	post(t, httpServer, "/api/ask", `{"session":"/sessions/a.jsonl","prompt":"again"}`, http.StatusConflict)

	// Switch current session to B and start session B turn concurrently.
	post(t, httpServer, "/api/sessions/open", `{"path":"/sessions/b.jsonl"}`, http.StatusOK)
	post(t, httpServer, "/api/ask", `{"prompt":"hello B"}`, http.StatusAccepted)
	<-startedB

	resp, err := http.Get(httpServer.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state: %v", err)
	}
	var state struct {
		Session         string   `json:"session"`
		Running         bool     `json:"running"`
		RunningSessions []string `json:"runningSessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	resp.Body.Close()

	if state.Session != "/sessions/b.jsonl" || !state.Running {
		t.Fatalf("state = %+v, want session=b running=true", state)
	}
	if len(state.RunningSessions) != 2 || state.RunningSessions[0] != "/sessions/a.jsonl" || state.RunningSessions[1] != "/sessions/b.jsonl" {
		t.Fatalf("runningSessions = %v, want both a and b", state.RunningSessions)
	}

	close(releaseA)
	close(releaseB)

	seenDone := map[string]bool{}
	seenAgent := map[string]bool{}
	for len(seenDone) < 2 || len(seenAgent) < 2 {
		name, data := readFrame(t, reader)
		if name == "status" {
			var st statusEvent
			if err := json.Unmarshal([]byte(data), &st); err != nil {
				t.Fatalf("decode status: %v", err)
			}
			if !st.Running && st.Session != "" {
				seenDone[st.Session] = true
			}
		}
		if name == "agent" {
			var ev struct {
				Session string `json:"session"`
				Type    string `json:"type"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("decode agent: %v", err)
			}
			if ev.Session != "" {
				seenAgent[ev.Session] = true
			}
		}
	}
}

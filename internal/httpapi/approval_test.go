package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pi-go/internal/httpapi/web"
	"pi-go/internal/wire"
)

func TestAnApprovalBlocksUntilThePageAnswers(t *testing.T) {
	answered := make(chan Decision, 1)
	// server is assigned before any turn runs, so the closure below always
	// sees the value it needs; the gate can only ask from inside a turn.
	var server *Server
	server = New(Options{Turn: func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
		// The tool goroutine parks here, exactly as the permission gate does.
		answered <- server.Ask(Question{Subject: "writes /tmp/x", Tool: "write"})
		return Receipt{}, nil
	}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, err := http.DefaultClient.Get(httpServer.URL + "/api/events")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)

	post(t, httpServer, "/api/ask", `{"prompt":"write"}`, http.StatusAccepted)

	// The question must reach the page before anyone can answer it.
	var id string
	for id == "" {
		name, data := readFrame(t, reader)
		if name != "approval" {
			continue
		}
		var payload struct {
			ID   string `json:"id"`
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("decode approval: %v", err)
		}
		if payload.Tool != "write" {
			t.Fatalf("approval tool = %q", payload.Tool)
		}
		id = payload.ID
	}

	post(t, httpServer, "/api/approval", `{"id":"`+id+`","allowed":true}`, http.StatusNoContent)

	select {
	case decision := <-answered:
		if !decision.Allowed {
			t.Fatalf("decision = %+v, want allowed", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the tool goroutine never resumed")
	}
}

func TestTheSecondAnswerForOneQuestionIsRefused(t *testing.T) {
	// Two POSTs for the same id must not both count: the tool already acted on
	// the first, and a "deny" arriving afterwards would read as a refusal that
	// never happened.
	server := New(Options{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	server.approvals.add(Question{Subject: "s", Tool: "bash"})
	post(t, httpServer, "/api/approval", `{"id":"a1","allowed":true}`, http.StatusNoContent)
	post(t, httpServer, "/api/approval", `{"id":"a1","allowed":false}`, http.StatusConflict)
}

func TestStateCarriesTheOpenQuestions(t *testing.T) {
	// A reload loses the stream but not the question, so the redrawn page asks
	// the server what should be on screen.
	server := New(Options{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	server.approvals.add(Question{Subject: "writes /tmp/x", Tool: "write"})

	response, err := http.DefaultClient.Get(httpServer.URL + "/api/state")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	defer response.Body.Close()

	var state struct {
		Pending []struct {
			ID   string `json:"id"`
			Tool string `json:"tool"`
		} `json:"pending"`
	}
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Pending) != 1 || state.Pending[0].Tool != "write" {
		t.Fatalf("pending = %+v", state.Pending)
	}
}

func TestClosingTheServerDeniesOpenQuestions(t *testing.T) {
	// A window that quits while a box is open must not leave a turn parked
	// forever: shutdown is a no.
	server := New(Options{})
	done := make(chan Decision, 1)
	go func() { done <- server.Ask(Question{Subject: "s", Tool: "write"}) }()

	deadline := time.Now().Add(2 * time.Second)
	for len(server.approvals.list()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(server.approvals.list()) != 1 {
		t.Fatal("question never registered")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case decision := <-done:
		if decision.Allowed {
			t.Fatalf("decision = %+v, want denied", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after Close")
	}
}

// TestThePageDrawsAnApprovalBox guards the contract between this package and
// its own frontend: the document is served, and the built bundle talks to the
// approval route above.
//
// The check moved from the document to the bundle when the inline page became a
// built app: the box is a component now, so the thing worth asserting is that
// the artifact the binary carries still posts to this route.
func TestThePageDrawsAnApprovalBox(t *testing.T) {
	server := New(Options{Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
		return Receipt{}, nil
	}})
	defer server.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	// The guard refuses a non-loopback Host, which is the point of it; a test
	// client has to arrive the way the window does.
	request.Host = "127.0.0.1:7788"
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET / content type = %q, want html", recorder.Header().Get("Content-Type"))
	}

	assets, err := fs.Glob(web.Assets(), "assets/*.js")
	if err != nil {
		t.Fatalf("glob assets: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("the build has no javascript bundle")
	}
	joined := ""
	for _, name := range assets {
		body, err := fs.ReadFile(web.Assets(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		joined += string(body)
	}
	for _, want := range []string{"/api/approval", "/api/ask"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bundle is missing %q", want)
		}
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"pi-go/internal/wire"
)

// sideHost answers the three questions the side routes ask, and records what
// they asked for. Every other method of the interface is left to the embedded
// nil: a route that reached for one of them would panic, which is the answer a
// test wants.
type sideHost struct {
	Host
	mu     sync.Mutex
	view   SideView
	opens  int
	closes int
}

func (h *sideHost) Side() (SideView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.view, nil
}

func (h *sideHost) OpenSide() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opens++
	h.view = SideView{Open: true, Inherited: 2, Blocks: []TranscriptBlock{
		{Role: "user", Text: "earlier"},
		{Role: "answer", Text: "said"},
	}}
	return nil
}

func (h *sideHost) CloseSide() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closes++
	h.view = SideView{}
	return nil
}

func (h *sideHost) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.opens, h.closes
}

func idleTurn(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
	return Receipt{}, nil
}

// A compaction answers when it is over rather than when it starts: it rewrites
// the context the next turn reads, so a window that got its 200 early would
// accept a message that then used the history the reader just asked to be rid
// of. The status frames are the progress bar, and they bracket the work.
func TestCompactAnswersWhenTheWorkIsOver(t *testing.T) {
	var notes []string
	server := New(Options{Compact: func(ctx context.Context, note string, emit func(wire.AgentEvent) error) error {
		notes = append(notes, note)
		return emit(wire.AgentEvent{Type: wire.AgentEventCompactionStart, Reason: wire.CompactionReasonManual})
	}})
	httpServer, reader, _ := openStream(t, server)
	readFrame(t, reader) // hello

	post(t, httpServer, "/api/compact", `{"note":"keep the API part"}`, http.StatusOK)

	// The answer is written after the work, so every frame is already queued and
	// the order is the order they were published in.
	for index, want := range []string{"status", "agent", "status"} {
		name, _ := readFrame(t, reader)
		if name != want {
			t.Fatalf("frame %d = %q, want %q", index, name, want)
		}
	}
	if len(notes) != 1 || notes[0] != "keep the API part" {
		t.Fatalf("notes = %q, want the note that was typed", notes)
	}
}

// A compaction with no note is the common case, not a malformed request.
func TestCompactAcceptsAnEmptyBody(t *testing.T) {
	ran := false
	server := New(Options{Compact: func(context.Context, string, func(wire.AgentEvent) error) error {
		ran = true
		return nil
	}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	post(t, httpServer, "/api/compact", "", http.StatusOK)
	if !ran {
		t.Fatal("compaction did not run for an empty body")
	}
}

// A compaction that fails is reported once, in the conversation: the kernel's
// compaction_end carries the reason, so the status frame and the answer stay
// quiet about it. Repeating it in all three places made one failure look like
// three.
func TestCompactReportsAFailureOnlyInTheConversation(t *testing.T) {
	server := New(Options{Compact: func(context.Context, string, func(wire.AgentEvent) error) error {
		return errors.New("no summary")
	}})
	httpServer, reader, _ := openStream(t, server)
	readFrame(t, reader) // hello

	// 200, not 409: the request ran. What happened is in the stream.
	post(t, httpServer, "/api/compact", "", http.StatusOK)

	readFrame(t, reader) // status, running
	name, data := readFrame(t, reader)
	if name != "status" {
		t.Fatalf("frame = %q, want status", name)
	}
	var status statusEvent
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Running {
		t.Fatalf("status = %+v, want running false", status)
	}
	if status.Error != "" {
		t.Fatalf("status carries %q; compaction_end already said it", status.Error)
	}
}

// The kernel holds one turn at a time and refuses a compaction under one, so the
// window answers the same way instead of letting the second request fail deeper
// in, where the reason would arrive as a broken stream.
func TestCompactIsRefusedWhileATurnRuns(t *testing.T) {
	release := make(chan struct{})
	server := New(Options{
		Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
			<-release
			return Receipt{}, nil
		},
		Compact: func(context.Context, string, func(wire.AgentEvent) error) error {
			t.Error("compaction ran under a running turn")
			return nil
		},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	post(t, httpServer, "/api/ask", `{"prompt":"hi"}`, http.StatusAccepted)
	defer close(release)

	post(t, httpServer, "/api/compact", "", http.StatusConflict)
}

// Opening twice keeps the conversation. BeginSide replaces what is open — that is
// what makes /side a way to start over in the terminal — but a page that redraws,
// or a reader who types /side again to look at the panel, is not asking to throw
// away what is already there.
func TestOpeningTheSideConversationTwiceKeepsIt(t *testing.T) {
	host := &sideHost{}
	server := newSideServer(t, host, nil)
	defer server.Close()

	for attempt := 0; attempt < 2; attempt++ {
		response := postJSON(t, server.URL+"/api/side", "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("open %d = %d, want 200", attempt, response.StatusCode)
		}
		var state sideState
		if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
			t.Fatalf("decode open %d: %v", attempt, err)
		}
		if !state.Open || state.Inherited != 2 || len(state.Blocks) != 2 {
			t.Fatalf("open %d = %+v, want the conversation kept", attempt, state)
		}
	}
	if opens, _ := host.counts(); opens != 1 {
		t.Fatalf("OpenSide called %d times, want 1", opens)
	}
}

// The side agent is a second agent, so a question to it must not wait for the
// main turn to end. The two latches are separate on purpose, and this is the
// behaviour that would regress if they were merged back into one.
func TestASideTurnDoesNotWaitForTheMainOne(t *testing.T) {
	release := make(chan struct{})
	host := &sideHost{}
	server := New(Options{
		Host: host,
		Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
			<-release
			return Receipt{}, nil
		},
		SideTurn: func(_ context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
			if prompt != "what about the tests" {
				t.Errorf("prompt = %q", prompt)
			}
			return Receipt{Fields: []string{"12 tok"}}, emit(wire.AgentEvent{Type: wire.AgentEventMessageEnd})
		},
	})
	httpServer, reader, _ := openStream(t, server)
	readFrame(t, reader) // hello

	post(t, httpServer, "/api/ask", `{"prompt":"hi"}`, http.StatusAccepted)
	defer close(release)
	post(t, httpServer, "/api/side", "", http.StatusOK)
	post(t, httpServer, "/api/side/ask", `{"prompt":"what about the tests"}`, http.StatusAccepted)

	// status (the main turn), the open's own frame, then the side's bracket around
	// its own work.
	if name, _ := readFrame(t, reader); name != "status" {
		t.Fatalf("first frame = %q, want the main turn's status", name)
	}
	for index, want := range []string{"sidestatus", "sidestatus", "sideagent", "sidestatus"} {
		name, _ := readFrame(t, reader)
		if name != want {
			t.Fatalf("side frame %d = %q, want %q", index, name, want)
		}
	}
}

// The side conversation's events have their own name. A page folds one stream
// into the transcript and the other into the panel, and a marker inside every
// frame would be the same fold with one more condition per case.
func TestTheSideStreamCarriesTheReceiptThatEndsIt(t *testing.T) {
	host := &sideHost{}
	server := New(Options{
		Host: host,
		SideTurn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
			return Receipt{Fields: []string{"12 tok", "0.3s"}}, nil
		},
	})
	httpServer, reader, _ := openStream(t, server)
	readFrame(t, reader) // hello

	post(t, httpServer, "/api/side", "", http.StatusOK)
	readFrame(t, reader) // the open publishes its own status
	post(t, httpServer, "/api/side/ask", `{"prompt":"hi"}`, http.StatusAccepted)

	readFrame(t, reader) // sidestatus, running
	name, data := readFrame(t, reader)
	if name != "sidestatus" {
		t.Fatalf("frame = %q, want sidestatus", name)
	}
	var status sideStatus
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Running || !status.Open || len(status.Receipt) != 2 {
		t.Fatalf("status = %+v, want a closed side turn carrying its receipt", status)
	}
}

// A question with no conversation to land in is refused here rather than in the
// kernel, where it would arrive as a turn that produced nothing.
func TestASideTurnNeedsAnOpenConversation(t *testing.T) {
	host := &sideHost{}
	server := newSideServer(t, host, idleTurn)
	defer server.Close()

	post(t, server, "/api/side/ask", `{"prompt":"hi"}`, http.StatusConflict)
}

// Closing under a running turn would take the conversation away from the answer
// the reader is watching arrive, and nothing on screen could tell that from a
// crash.
func TestClosingTheSideConversationIsRefusedWhileItAnswers(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	host := &sideHost{}
	server := New(Options{
		Host: host,
		SideTurn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
			close(started)
			<-release
			return Receipt{}, nil
		},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	post(t, httpServer, "/api/side", "", http.StatusOK)
	post(t, httpServer, "/api/side/ask", `{"prompt":"hi"}`, http.StatusAccepted)
	<-started

	post(t, httpServer, "/api/side/close", "", http.StatusConflict)
	close(release)
	if _, closes := host.counts(); closes != 0 {
		t.Fatalf("CloseSide called %d times, want 0", closes)
	}
}

func TestClosingTheSideConversationDiscardsIt(t *testing.T) {
	host := &sideHost{}
	server := newSideServer(t, host, idleTurn)
	defer server.Close()

	post(t, server, "/api/side", "", http.StatusOK)
	post(t, server, "/api/side/close", "", http.StatusOK)

	response, err := http.Get(server.URL + "/api/side")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	var state sideState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Open || len(state.Blocks) != 0 {
		t.Fatalf("state = %+v, want a closed conversation", state)
	}
	// An empty conversation is a list, not null: the panel folds the answer and a
	// null would make it branch on a shape the server is responsible for.
	if state.Blocks == nil {
		t.Fatal("blocks = null, want an empty list")
	}
}

// -offline has no kernel behind these routes, and a build that has none says so
// instead of pretending the work happened.
func TestCompactionAndTheSideConversationSaySoWhenThereIsNoKernel(t *testing.T) {
	server := newSideServer(t, &sideHost{}, nil)
	defer server.Close()

	post(t, server, "/api/compact", "", http.StatusNotImplemented)
	post(t, server, "/api/side/ask", `{"prompt":"hi"}`, http.StatusNotImplemented)
}

// newSideServer is the same server projectServer builds, with the two actions
// these routes need filled in. A nil action is how a test says "-offline".
func newSideServer(t *testing.T, host Host, sideTurn Turn) *httptest.Server {
	t.Helper()
	options := Options{Turn: idleTurn, Host: host, SideTurn: sideTurn}
	if sideTurn != nil {
		options.Compact = func(context.Context, string, func(wire.AgentEvent) error) error { return nil }
	}
	httpServer := httptest.NewServer(New(options).Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

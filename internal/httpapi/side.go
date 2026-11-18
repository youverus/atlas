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
	Note string `json:"note"`
}

// serveCompact runs one manual compaction and answers when it is over.
//
// Synchronous, unlike a turn. A compaction rewrites the context the next turn
// reads, so a window that returned before it finished would accept a message
// that then used the old context — the reader would see their question answered
// by a history they had just asked to be rid of. The reader watches it happen on
// the event stream either way (compaction_start, then compaction_end), so the
// wait costs nothing but the wait.
func (s *Server) serveCompact(w http.ResponseWriter, r *http.Request) {
	if s.compact == nil {
		http.Error(w, "httpapi: no compact configured", http.StatusNotImplemented)
		return
	}
	var request compactRequest
	// An empty body is a compaction with no note, not a malformed request.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The turn latch, not a third one: the kernel refuses a turn while it is
	// compacting and refuses a compaction while a turn runs, so the window must
	// answer the same way instead of letting the second request fail deeper in.
	if !s.running.CompareAndSwap(false, true) {
		http.Error(w, "httpapi: a turn is already running", http.StatusConflict)
		return
	}
	defer s.running.Store(false)

	s.publishStatus(statusEvent{Running: true})
	err := s.compact(context.Background(), request.Note, s.emit)
	if err != nil {
		// The failure is already in the conversation: the kernel emits
		// compaction_end carrying the reason, and the window draws it where the
		// compaction started. Saying it again here — on the status frame and in
		// the HTTP answer — reported one failure three times in three voices,
		// which the reader sees as three problems.
		//
		// So the answer stays 200: the request was accepted and did run, and
		// "what happened" is a fact about the conversation, not the transport.
		// Only a rejection that never ran anything (the turn latch above) is an
		// HTTP error, because nothing was emitted to carry that news.
		s.publishStatus(statusEvent{Running: false})
		writeJSON(w, map[string]bool{"ok": false})
		return
	}
	s.publishStatus(statusEvent{Running: false})
	writeJSON(w, map[string]bool{"ok": true})
}

// sideStatus is the side conversation's own status frame. It is a separate
// event name from status because the two conversations run at the same time: a
// page that folded them into one "running" flag would disable the main composer
// while the side is thinking.
type sideStatus struct {
	Open    bool     `json:"open"`
	Running bool     `json:"running"`
	Error   string   `json:"error,omitempty"`
	Receipt []string `json:"receipt,omitempty"`
}

// sideState is what GET /api/side answers: the conversation, plus the two facts
// only this server knows.
type sideState struct {
	SideView
	Running bool `json:"running"`
}

// serveSide reads the side conversation. A page that reloads mid-turn calls this
// and gets the messages recorded so far: the side agent appends as it goes, so
// the answer is the same one the stream would have produced.
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
	writeJSON(w, sideState{SideView: view, Running: s.sideRunning.Load()})
}

// serveSideOpen takes the snapshot, or reports the one that is already open.
func (s *Server) serveSideOpen(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	// Opening twice must not discard the first one. BeginSide replaces whatever
	// is open — that is what makes /side in the terminal a way to start over —
	// but a page that redraws, or a reader who types /side again to look at the
	// panel, is not asking to throw the conversation away.
	if view, err := s.host.Side(); err == nil && view.Open {
		writeJSON(w, sideState{SideView: view, Running: s.sideRunning.Load()})
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
	s.publishSideStatus(sideStatus{Open: true, Running: s.sideRunning.Load()})
	writeJSON(w, sideState{SideView: view, Running: s.sideRunning.Load()})
}

// serveSideAsk starts one turn on the side conversation.
//
// The same shape as /api/ask, and deliberately not the same latch: the side
// agent is a second agent, and a question to it must not wait for the main turn
// to finish.
func (s *Server) serveSideAsk(w http.ResponseWriter, r *http.Request) {
	if s.sideTurn == nil {
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
	if !s.sideRunning.CompareAndSwap(false, true) {
		http.Error(w, "httpapi: a side turn is already running", http.StatusConflict)
		return
	}
	prompt := request.Prompt
	go s.runSideTurn(prompt)
	w.WriteHeader(http.StatusAccepted)
}

// runSideTurn drives the injected side turn and brackets it with side status
// frames, for the reason runTurn does: a page that only knows agent events
// cannot tell a failed side turn from a slow one.
func (s *Server) runSideTurn(prompt string) {
	defer s.sideRunning.Store(false)

	s.publishSideStatus(sideStatus{Open: true, Running: true})
	receipt, err := s.sideTurn(context.Background(), prompt, s.emitSide)
	done := sideStatus{Open: true, Running: false, Receipt: receipt.Fields}
	if err != nil {
		done.Error = err.Error()
	}
	s.publishSideStatus(done)
}

// serveSideClose discards the side conversation.
//
// Refused while a side turn runs: the kernel closes the conversation under the
// turn, and the reader would lose the answer they are watching arrive with no
// way to tell that from a crash.
func (s *Server) serveSideClose(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	if s.sideRunning.Load() {
		http.Error(w, "httpapi: a side turn is running", http.StatusConflict)
		return
	}
	if err := s.host.CloseSide(); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishSideStatus(sideStatus{Open: false})
	writeJSON(w, map[string]bool{"ok": true})
}

// emitSide publishes one agent event of the side conversation.
//
// A distinct event name, not a field on the frame: the page folds the main
// stream into the transcript and this one into the panel, and a fold that had to
// branch on a marker inside every event would be the same code with one more
// condition per case.
func (s *Server) emitSide(event wire.AgentEvent) error {
	payload, err := json.Marshal(event)
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

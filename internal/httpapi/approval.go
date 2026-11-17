package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// approvalTimeout bounds one question. The turn is blocked on the tool
// goroutine for as long as nobody answers, so a window that was closed
// without a clean shutdown would otherwise leave the turn parked forever.
// It is long on purpose: a reader may step away mid-approval, and denying a
// call they were about to allow loses their work.
const approvalTimeout = 10 * time.Minute

// Question is one tool call waiting for a human. It is this package's own
// shape rather than the composition root's: httpapi may reach wire and
// nothing else, so the shell adapts its Ask to this — the same inversion Turn
// already uses for the provider side.
type Question struct {
	// Subject is the sentence the reader decides on.
	Subject string `json:"subject"`
	// Tool is what an "always" answer is remembered against.
	Tool string `json:"tool"`
}

// Decision is what the reader said. Always is the third state: yes, and stop
// asking for this tool for the rest of the session.
type Decision struct {
	Allowed bool `json:"allowed"`
	Always  bool `json:"always"`
}

// pending is one open question: what to draw, and where the answer goes.
type pending struct {
	Question
	id     string
	answer chan Decision
}

// approvals holds the questions in flight.
//
// It is a registry rather than a single slot because the page may be reloaded
// while a question is open: the answer arrives by id, so a redrawn box can
// still close the call it was drawn for. Publishing and retrieving are
// separate locks-free operations on the same map, which is why the mutex is
// held for one map access at a time.
type approvals struct {
	mu      sync.Mutex
	next    int
	open    map[string]*pending
	order   []string
	closed  bool
	changed chan struct{}
}

func newApprovals() *approvals {
	return &approvals{
		open:    make(map[string]*pending),
		changed: make(chan struct{}),
	}
}

// add registers a question and returns it. A closed server refuses: the
// caller then denies, because a window that is gone cannot approve anything.
func (a *approvals) add(question Question) (*pending, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, false
	}
	a.next++
	item := &pending{
		Question: question,
		id:       fmt.Sprintf("a%d", a.next),
		answer:   make(chan Decision, 1),
	}
	a.open[item.id] = item
	a.order = append(a.order, item.id)
	a.notify()
	return item, true
}

// settle removes a question and delivers the answer exactly once. A second
// POST for the same id is refused rather than counted twice: the first answer
// is the one the tool acted on.
func (a *approvals) settle(id string, decision Decision) bool {
	a.mu.Lock()
	item, ok := a.open[id]
	if !ok {
		a.mu.Unlock()
		return false
	}
	delete(a.open, id)
	a.dropOrder(id)
	a.notify()
	a.mu.Unlock()

	// Buffered, and removed from the map above, so this cannot block or race
	// with the reader of the channel.
	item.answer <- decision
	return true
}

// forget removes a question whose reader went away without answering.
func (a *approvals) forget(id string) {
	a.mu.Lock()
	delete(a.open, id)
	a.dropOrder(id)
	a.notify()
	a.mu.Unlock()
}

// dropOrder removes an id from the insertion order. Callers hold the lock.
func (a *approvals) dropOrder(id string) {
	for i, existing := range a.order {
		if existing == id {
			a.order = append(a.order[:i], a.order[i+1:]...)
			return
		}
	}
}

// notify wakes every waiter on changed. Callers hold the lock.
func (a *approvals) notify() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// list reports the open questions in the order they were asked, so a page
// that reloads redraws the same box rather than an empty screen facing a
// blocked turn.
func (a *approvals) list() []pending {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]pending, 0, len(a.order))
	for _, id := range a.order {
		if item, ok := a.open[id]; ok {
			out = append(out, *item)
		}
	}
	return out
}

// close denies everything still open. It runs on window shutdown so a turn
// blocked on a question ends instead of outliving the window.
func (a *approvals) close() {
	a.mu.Lock()
	a.closed = true
	items := make([]*pending, 0, len(a.open))
	for _, id := range a.order {
		if item, ok := a.open[id]; ok {
			items = append(items, item)
		}
	}
	a.open = make(map[string]*pending)
	a.order = nil
	a.notify()
	a.mu.Unlock()

	for _, item := range items {
		select {
		case item.answer <- Decision{}:
		default:
		}
	}
}

// Ask blocks one tool call until the reader answers.
//
// It is what the shell hands to the composition root as Options.Ask. Denying
// is the zero value and the fallback: no window, a closed server, or a
// timeout all mean the same thing, and guessing yes is the answer that loses
// data.
func (s *Server) Ask(question Question) Decision {
	item, ok := s.approvals.add(question)
	if !ok {
		return Decision{}
	}
	defer s.approvals.forget(item.id)

	if payload, err := json.Marshal(struct {
		ID string `json:"id"`
		Question
	}{ID: item.id, Question: question}); err == nil {
		s.broker.publish(streamEvent{Name: "approval", Data: payload})
	}

	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()

	select {
	case decision := <-item.answer:
		return decision
	case <-timer.C:
		return Decision{}
	}
}

// approvalRequest is the POST body. The id comes from the approval event, so
// a redrawn box can answer the call it was drawn for rather than whichever
// one happens to be open now.
type approvalRequest struct {
	ID      string `json:"id"`
	Allowed bool   `json:"allowed"`
	Always  bool   `json:"always"`
}

// serveApproval answers one open question. It is a POST rather than a query
// parameter because answering a permission change is not a safe operation,
// and it returns no body: the outcome is already on the stream as the tool
// that follows.
func (s *Server) serveApproval(w http.ResponseWriter, r *http.Request) {
	var request approvalRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.ID == "" {
		http.Error(w, "httpapi: empty approval id", http.StatusBadRequest)
		return
	}
	decision := Decision{Allowed: request.Allowed, Always: request.Always}
	if !s.approvals.settle(request.ID, decision) {
		http.Error(w, "httpapi: no such approval (already answered or withdrawn)", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package channel

import (
	"sync"
	"time"

	"pi-go/internal/assembly"
)

// approvals holds the one open tool-approval question per chat. The kernel's
// gate parks the tool goroutine on the Ask callback; this is where that wait
// lives while the chat keeps receiving messages.
//
// One open question per key, because one chat runs one turn at a time: a new
// question denies the old one rather than queueing behind it — an approval
// that arrived out of order is one the reader is no longer looking at.
type approvals struct {
	mu sync.Mutex
	m  map[string]*approvalPending
}

type approvalPending struct {
	question string
	answer   chan assembly.Answer
}

func newApprovals() *approvals {
	return &approvals{m: map[string]*approvalPending{}}
}

func (a *approvals) open(key, question string) *approvalPending {
	a.mu.Lock()
	defer a.mu.Unlock()
	if old, ok := a.m[key]; ok {
		old.answer <- assembly.Answer{} // denied: the reader moved on
	}
	p := &approvalPending{question: question, answer: make(chan assembly.Answer, 1)}
	a.m[key] = p
	return p
}

// close drops the pending question without answering it. Called after the
// wait already settled, or when the turn died another way.
func (a *approvals) close(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, key)
}

// settle routes a chat answer ("y" / "a" / "n") to the waiting question and
// reports whether there was one.
func (a *approvals) settle(key string, ans assembly.Answer) bool {
	a.mu.Lock()
	p, ok := a.m[key]
	a.mu.Unlock()
	if !ok {
		return false
	}
	p.answer <- ans
	return true
}

// wait blocks until the chat answers, the turn ends, or the budget runs out.
// Every exit path that is not an explicit answer is a denial — the same
// fallback the window's approval gate uses.
func (a *approvals) wait(p *approvalPending, done <-chan struct{}, budget time.Duration) assembly.Answer {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case ans := <-p.answer:
		return ans
	case <-done:
		return assembly.Answer{}
	case <-timer.C:
		return assembly.Answer{}
	}
}

// pending reports whether key has an open question — the inbound router asks
// this before treating "y" as prose.
func (a *approvals) pending(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.m[key]
	return ok
}

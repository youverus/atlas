package httpapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"pi-go/internal/wire"
)

type subagentTurnProbe struct {
	pending atomic.Bool
	started chan string
}

func (p *subagentTurnProbe) SubagentResultsPending(session string) bool {
	return session == "origin" && p.pending.Load()
}

func (p *subagentTurnProbe) RunSubagentResults(_ context.Context, session string, _ func(wire.AgentEvent) error) (Receipt, error) {
	p.pending.Store(false)
	p.started <- session
	return Receipt{}, nil
}

func (*subagentTurnProbe) StopSubagent(string, string) error { return nil }

func TestSubagentCompletionWaitsForSessionTurnSlot(t *testing.T) {
	probe := &subagentTurnProbe{started: make(chan string, 2)}
	server := New(Options{Subagents: probe})
	t.Cleanup(func() { _ = server.Close() })
	run := server.startTurn(turnKey{session: "origin"})
	if run == nil {
		t.Fatal("could not start originating turn")
	}
	probe.pending.Store(true)
	server.WakeSubagents("other")
	server.WakeSubagents("origin")
	select {
	case session := <-probe.started:
		t.Fatalf("started before original turn finished: %s", session)
	default:
	}
	server.finishTurn(run, nil, nil)
	server.WakeSubagents("origin")
	select {
	case session := <-probe.started:
		if session != "origin" {
			t.Fatalf("started wrong session: %s", session)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed task did not wake its originating session")
	}
}

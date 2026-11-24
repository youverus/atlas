package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

type completedSubagent struct {
	updates []wire.SubagentUpdate
}

func (r completedSubagent) Prepare(SubagentTurn) (tool.Tool, func()) { return nil, nil }
func (r completedSubagent) Commit(string, string, string)            {}
func (r completedSubagent) Completed(_, _ string) ([]wire.SubagentUpdate, error) {
	return r.updates, nil
}

func TestSubagentReportIsDeliveredOnceOnItsBranch(t *testing.T) {
	ctx := context.Background()
	manager := newSession(t)
	base, err := manager.AppendMessage(wire.UserMessage("review"))
	if err != nil {
		t.Fatal(err)
	}
	launch, err := manager.AppendMessage(wire.UserMessage("launch anchor"))
	if err != nil {
		t.Fatal(err)
	}
	prov := faux.New(faux.Response{Text: "integrated"})
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Subagents = completedSubagent{updates: []wire.SubagentUpdate{
			{ID: "task-live", EntryID: launch, Role: wire.SubagentVerifier, State: "succeeded", Report: "evidence here", Usage: &wire.Usage{Input: 19}},
			{ID: "task-other", EntryID: "elsewhere", Role: wire.SubagentExplorer, State: "succeeded", Report: "other branch", Usage: &wire.Usage{Input: 23}},
		}}
	})
	if !application.HasSubagentResults() {
		t.Fatal("active branch completion was not runnable")
	}
	result, err := application.ProcessSubagentResults(ctx, nil)
	if err != nil || result.Text() != "integrated" {
		t.Fatalf("result %q, %v", result.Text(), err)
	}
	calls := prov.Calls()
	if len(calls) != 1 || len(calls[0].Messages) == 0 || !strings.Contains(calls[0].Messages[len(calls[0].Messages)-1].Text(), "evidence here") {
		t.Fatalf("completion missing from request: %#v", calls)
	}
	if _, err := application.ProcessSubagentResults(ctx, nil); !errors.Is(err, ErrNoSubagentResults) {
		t.Fatalf("repeat result turn: %v", err)
	}
	if !strings.Contains(application.Messages()[2].Text(), "Atlas runtime notification") || application.Messages()[2].Subagent == nil {
		t.Fatal("completion lost its origin")
	}
	entries := manager.Entries()
	if entries[2].Type != session.TypeUsage || entries[3].CustomType != wire.SubagentCompletion {
		t.Fatalf("completion was not persisted through the task projection: %#v", entries)
	}
	if err := application.Branch(base); err != nil {
		t.Fatal(err)
	}
	if application.HasSubagentResults() {
		t.Fatal("same result was offered on the branch before its launch")
	}
	stats, err := application.SessionStats()
	if err != nil || stats.Tokens.Input != 42 {
		t.Fatalf("subagent usage should count both branches once: %+v, %v", stats.Tokens, err)
	}
}

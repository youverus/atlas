//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
	"pi-go/internal/wire"
)

func TestKernelSharesSubagentsWithLiveSession(t *testing.T) {
	t.Setenv("ATLAS_HOME", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, cfgPath, `{"providers":{"local":{"api":"openai-completions","models":[{"id":"main"}]}}}`)
	ctx := context.Background()
	kernel, _, err := New(ctx, Options{ConfigPath: cfgPath, Workdir: t.TempDir(), NoMCP: true, EnableSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	defer kernel.Close()
	live, err := kernel.OpenLiveSession("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan wire.SubagentUpdate, 16)
	kernel.SetSubagentObserver(func(up wire.SubagentUpdate) { updates <- up })
	for _, application := range []*app.App{kernel.App, live.App} {
		prov := faux.New(
			faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("delegate", wire.SubagentToolName, json.RawMessage(`{"subagent_type":"explorer","description":"Trace it","prompt":"Inspect the workspace"}`))}},
			faux.Response{Text: "parent continues"},
			faux.Response{Text: "parent incorporated evidence"},
		)
		if err := application.ReloadConfig(app.RuntimeConfig{
			Model: wire.Model{ID: "parent", Provider: "fake"}, Provider: prov, OverrideModel: true,
		}); err != nil {
			t.Fatal(err)
		}
		child := faux.New(faux.Response{Text: "child evidence"})
		turn := app.WithSubagentModelOverride(ctx, app.SubagentModelOverride{
			AllowBackground: true, Model: wire.Model{ID: "override", Provider: "fake"}, Provider: child, ThinkingLevel: "off",
		})
		if result, err := application.AskInteractive(turn, wire.UserMessage("delegate the investigation"), nil, nil); err != nil || result.Text() != "parent continues" {
			t.Fatalf("parent turn = %q, %v", result.Text(), err)
		}
		final := waitSubagent(t, updates, subagentSucceeded)
		if final.SessionID != application.SessionID() || final.Model != "override" || final.Report != "child evidence" || !application.HasSubagentResults() {
			t.Fatalf("child result did not reach its parent: %+v", final)
		}
		if result, err := application.ProcessSubagentResults(ctx, nil); err != nil || result.Text() != "parent incorporated evidence" {
			t.Fatalf("completion turn = %q, %v", result.Text(), err)
		}
	}
}

func TestSubagentCommitRunsFreshScopedChild(t *testing.T) {
	cwd := t.TempDir()
	parent, err := session.Create(cwd, t.TempDir(), session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.AppendMessage(wire.UserMessage("parent history marker")); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(cwd, "inside.txt"), "workspace evidence")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeConfigFile(t, outside, "outside evidence")
	store, err := permission.Parse([]byte(`{"version":2,"ask":[{"tool_name":"ls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	runner := newSubagentRunner(nil, permission.New(permission.Options{Mode: permission.ModeBypass, Store: store}))
	defer runner.close()
	updates := make(chan wire.SubagentUpdate, 16)
	runner.setObserver(func(up wire.SubagentUpdate) { updates <- up })
	tools := fs.Tools(fs.Options{Cwd: cwd})[:4] // read, write, edit, ls
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("read-in", "read", json.RawMessage(`{"path":"inside.txt"}`)),
			wire.ToolCallContent("read-out", "read", json.RawMessage(fmt.Sprintf(`{"path":%q}`, outside))),
			wire.ToolCallContent("ls", "ls", json.RawMessage(`{}`)),
		}, Usage: wire.Usage{Input: 50}},
		faux.Response{Text: "verifier report", Usage: wire.Usage{Output: 25}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	impl, cleanup := runner.Prepare(app.SubagentTurn{
		SessionID: parent.ID(), SessionFile: parent.File(), Cwd: cwd, Roles: []string{wire.SubagentVerifier},
		Model: wire.Model{ID: "original", Provider: "test", ContextWindow: 5000}, Provider: prov, ThinkingLevel: "off", Tools: tools,
	})
	defer cleanup()
	if _, err := impl.Execute(ctx, tool.Call{Arguments: json.RawMessage(`{"subagent_type":"explorer","description":"trace","prompt":"check"}`)}, nil); err == nil {
		t.Fatal("launch accepted a role outside the requested set")
	}
	queued, receipt := launchSubagent(t, ctx, impl, "launch")
	if queued.State != subagentQueued || queued.Role != wire.SubagentVerifier || queued.ToolCallID != "launch" {
		t.Fatalf("queued receipt = %+v", queued)
	}
	if len(prov.Calls()) != 0 {
		t.Fatal("child started without Commit")
	}
	entry, err := parent.AppendMessage(receipt)
	if err != nil {
		t.Fatal(err)
	}
	// The selected provider and thinking level were frozen before this reload.
	runner.configure(&config.Config{Subagents: &config.Subagents{Default: config.SubagentModel{Model: "missing"}}})
	cancel()
	runner.Commit(parent.ID(), queued.ID, entry)
	cleanup()
	final := waitSubagent(t, updates, subagentSucceeded)
	if final.EntryID != entry || final.Report != "verifier report" || final.Usage == nil || final.Usage.Input != 50 || final.Usage.Output != 25 {
		t.Fatalf("terminal update = %+v", final)
	}
	calls := prov.Calls()
	if len(calls) != 2 || calls[0].Model.ID != "original" || calls[0].Options.ThinkingLevel != "off" || len(calls[0].Messages) != 2 || calls[0].Messages[1].Text() != "child-only prompt" {
		t.Fatalf("child did not receive the frozen model and fresh context: %+v", calls)
	}
	var declared []string
	for _, spec := range wire.CurrentTools(calls[0].Messages) {
		declared = append(declared, spec.Name)
	}
	if !reflect.DeepEqual(declared, []string{"read", "ls"}) {
		t.Fatalf("child declarations = %v", declared)
	}
	results := make(map[string]wire.Message)
	for _, msg := range calls[1].Messages {
		if msg.Role == wire.RoleToolResult {
			results[msg.ToolCallID] = msg
		}
	}
	if !strings.Contains(results["read-in"].Text(), "workspace evidence") || strings.Contains(results["read-out"].Text(), "outside evidence") || !strings.Contains(results["ls"].Text(), "approval required") {
		t.Fatalf("tool results escaped the scoped permission boundary: %#v", results)
	}
	reopened := newSubagentRunner(nil, nil)
	if stored, err := reopened.Completed(parent.ID(), parent.File()); err != nil || len(stored) != 1 || stored[0].ID != queued.ID || stored[0].Report != final.Report {
		t.Fatalf("completion after reopening = %+v, err=%v", stored, err)
	}
}

func TestSubagentStopAndLostRecovery(t *testing.T) {
	parent, err := session.Create(t.TempDir(), t.TempDir(), session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.AppendMessage(wire.UserMessage("delegate verification")); err != nil {
		t.Fatal(err)
	}
	prov := &subagentBlockingProvider{started: make(chan struct{})}
	runner := newSubagentRunner(nil, nil)
	defer runner.close()
	updates := make(chan wire.SubagentUpdate, 16)
	kernel := &Kernel{subagents: runner}
	kernel.SetSubagentObserver(func(up wire.SubagentUpdate) { updates <- up })
	impl, cleanup := runner.Prepare(app.SubagentTurn{
		SessionID: parent.ID(), SessionFile: parent.File(), Cwd: parent.Cwd(),
		Model: wire.Model{ID: "blocking"}, Provider: prov,
	})
	defer cleanup()
	ctx := context.Background()
	queued, receipt := launchSubagent(t, ctx, impl, "launch")
	entry, err := parent.AppendMessage(receipt)
	if err != nil {
		t.Fatal(err)
	}
	other := newSubagentRunner(nil, nil)
	if done, err := other.Completed(parent.ID(), parent.File()); err != nil || len(done) != 0 {
		t.Fatalf("live uncommitted record should be preserved: %+v %v", done, err)
	}
	runner.Commit(parent.ID(), queued.ID, entry)
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not start")
	}
	if done, err := other.Completed(parent.ID(), parent.File()); err != nil || len(done) != 0 {
		t.Fatalf("live running record should be preserved: %+v %v", done, err)
	}
	if err := kernel.StopSubagent("another-session", queued.ID); err == nil {
		t.Fatal("stop accepted a different session")
	}
	if err := kernel.StopSubagent(parent.ID(), queued.ID); err != nil {
		t.Fatal(err)
	}
	if stopped := waitSubagent(t, updates, subagentCanceled); stopped.Error != errSubagentStopped.Error() || stopped.EntryID != entry {
		t.Fatalf("stopped task = %+v", stopped)
	}

	interrupted, receipt := launchSubagent(t, ctx, impl, "before-commit")
	interruptedEntry, err := parent.AppendMessage(receipt)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a dead owner after the receipt was written but before Commit.
	runner.close()
	for i := 0; i < 2; i++ {
		done, err := other.Completed(parent.ID(), parent.File())
		if err != nil || len(done) != 2 {
			t.Fatalf("recovered completions = %+v %v", done, err)
		}
		for _, up := range done {
			if up.ID == interrupted.ID && (up.State != subagentLost || up.EntryID != interruptedEntry || up.Error == "") {
				t.Fatalf("lost task = %+v", up)
			}
		}
	}
}

func TestSubagentRunEnforcesProviderBudgetAndContextWindow(t *testing.T) {
	prov := faux.New()
	progress := 0
	bounded := &subagentProvider{Provider: prov, progress: func() { progress++ }}
	req := provider.Request{Messages: []wire.Message{wire.UserMessage("This prompt does not fit.")}, Model: wire.Model{ContextWindow: 1}}
	if _, err := bounded.Stream(context.Background(), req); err == nil || len(prov.Calls()) != 0 {
		t.Fatalf("oversized request reached provider: %v", err)
	}
	req.Model.ContextWindow = 1000
	for i := 0; i < subagentRequestLimit; i++ {
		stream, err := bounded.Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
	}
	if _, err := bounded.Stream(context.Background(), req); err == nil || len(prov.Calls()) != subagentRequestLimit || progress != subagentRequestLimit-1 {
		t.Fatalf("provider budget: requests=%d progress=%d err=%v", len(prov.Calls()), progress, err)
	}
}

func TestSubagentCleanupDiscardsUncommittedTasks(t *testing.T) {
	runner := newSubagentRunner(nil, nil)
	defer runner.close()
	dir := t.TempDir()
	file := filepath.Join(dir, "parent.jsonl")
	prov := faux.New()
	impl, cleanup := runner.Prepare(app.SubagentTurn{
		SessionID: "parent", SessionFile: file, Cwd: dir, Model: wire.Model{ID: "test"}, Provider: prov,
	})
	var queued wire.SubagentUpdate
	for i := 0; i < subagentTurnLimit; i++ {
		queued, _ = launchSubagent(t, context.Background(), impl, fmt.Sprint(i))
	}
	if _, err := impl.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"subagent_type":"verifier","description":"test","prompt":"test"}`)}, nil); err == nil {
		t.Fatal("turn accepted more than its allowed delegations")
	}
	cleanup()
	runner.Commit("parent", queued.ID, "late")
	if len(prov.Calls()) > 0 {
		t.Fatal("the discarded job started")
	}
	if files, err := os.ReadDir(session.SubagentDir(file)); err != nil || len(files) != 0 {
		t.Fatalf("incomplete task records were left behind: %v %v", files, err)
	}
}

func launchSubagent(t *testing.T, ctx context.Context, impl tool.Tool, callID string) (wire.SubagentUpdate, wire.Message) {
	t.Helper()
	call := tool.Call{
		ID: callID, Name: impl.Name(), Arguments: json.RawMessage(`{"subagent_type":"verifier","description":"Check evidence","prompt":"child-only prompt"}`),
	}
	result, err := impl.Execute(ctx, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	var queued wire.SubagentUpdate
	if err := json.Unmarshal(result.Details, &queued); err != nil || queued.ID == "" {
		t.Fatalf("invalid receipt: %s %v", result.Details, err)
	}
	receipt := wire.ToolResultMessage(callID, impl.Name(), result.IsError, result.Content...)
	receipt.Details = result.Details
	return queued, receipt
}

func waitSubagent(t *testing.T, updates <-chan wire.SubagentUpdate, state string) wire.SubagentUpdate {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case up := <-updates:
			if up.State != subagentQueued && up.State != subagentRunning {
				if up.State != state {
					t.Fatalf("terminal update = %+v, want %s", up, state)
				}
				return up
			}
		case <-timer.C:
			t.Fatalf("no %s update", state)
		}
	}
}

type subagentBlockingProvider struct {
	started chan struct{}
}

func (*subagentBlockingProvider) API() string { return "blocking" }

func (p *subagentBlockingProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// TestBashRunsConfinedEndToEnd closes the loop the unit tests leave open. The
// tool the model calls, the sandbox the config builds and the kernel that
// enforces it are three packages, and each of them passing on its own says
// nothing about the seam between them: a policy that is compiled but never handed
// to the tool would pass every one of them.
func TestBashRunsConfinedEndToEnd(t *testing.T) {
	dir := t.TempDir()
	block := &config.Sandbox{}
	if applied, note := assembly.SandboxNote(block); !applied {
		t.Skipf("no sandbox on this machine: %s", note)
	}

	var bash tool.Tool
	for _, candidate := range workspaceTools(dir, nil, nil, block) {
		if candidate.Name() == "bash" {
			bash = candidate
		}
	}
	if bash == nil {
		t.Fatal("the tool set has no bash")
	}

	result, err := bash.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"echo $ATLAS_SANDBOXED > marker.txt && cat marker.txt"}`),
	}, func(tool.Partial) {})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if result.IsError {
		t.Fatalf("the confined command failed: %v", result.Content)
	}

	// The marker is what proves the command ran inside the profile rather than
	// beside it: it is set by the wrap and by nothing else.
	written, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
	if err != nil {
		t.Fatalf("the command wrote nothing: %v", err)
	}
	if got := strings.TrimSpace(string(written)); got != "1" {
		t.Fatalf("ATLAS_SANDBOXED = %q, want 1; the command ran unconfined", got)
	}
}

// TestAskStopsTheCall verifies the main conversation's permission seam end to
// end: when a write call hits an ask rule in permission.json and is declined,
// the file is not created and the model receives an actionable refusal.
func TestAskStopsTheCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	if err := os.WriteFile(filepath.Join(home, permission.FileName), []byte(`{
  "version": 2,
  "ask": [{"tool_name": "write"}]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	engine, note := assembly.PermissionFor(&config.Config{}, false, config.Selection{}, "")
	if note != "" {
		t.Fatalf("assembly.PermissionFor note = %q, want empty", note)
	}
	state := newSideStateWith(assembly.NewGate(engine))
	asked := 0
	state.approve = func(tool.Call) bool {
		asked++
		return false
	}

	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "write", json.RawMessage(`{"path":"blocked.txt","content":"secret"}`)),
		}},
		faux.Response{Text: "stopped"},
	)

	var events []wire.AgentEvent
	if _, err := application.Ask(context.Background(), wire.UserMessage("write it"), func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if asked != 1 {
		t.Fatalf("the main gate asked %d times, want 1", asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked.txt was written despite declined ask: %v", err)
	}
	reason := refusedReason(t, events, "c1")
	if !strings.Contains(reason, "declined") || !strings.Contains(reason, "do not retry") {
		t.Fatalf("model received %q, want an actionable refusal", reason)
	}
}

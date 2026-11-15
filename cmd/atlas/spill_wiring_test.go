package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/session"
	"pi-go/internal/tool"
)

// The wiring test for 0.12.8: bash's untruncated output goes beside the session
// that asked for it, not into a temp directory. The assertion is on the path the
// tool itself reports, so it fails if either the accumulator or the tool set
// stops carrying the spill.
func TestBashSpillLandsInTheSessionBucket(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "--proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manager, err := session.Create(t.TempDir(), dir, session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	spill := tool.NewSpill(func() string { return filepath.Join(manager.Dir(), spillDirName) })

	var bash tool.Tool
	for _, candidate := range workspaceToolsWithSpill(t.TempDir(), spill) {
		if candidate.Name() == "bash" {
			bash = candidate
		}
	}
	if bash == nil {
		t.Fatal("no bash tool in the set")
	}

	result, err := bash.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"seq 1 2500"}`),
	}, func(tool.Partial) {})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}

	var details struct {
		FullOutputPath string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.FullOutputPath == "" {
		t.Fatal("no full output path")
	}
	want := filepath.Join(dir, spillDirName)
	if got := filepath.Dir(details.FullOutputPath); got != want {
		t.Fatalf("spill in %q, want %q", got, want)
	}
	full, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if lines := strings.Count(string(full), "\n"); lines != 2500 {
		t.Fatalf("spill has %d lines, want 2500", lines)
	}

	// The bucket holds sessions and now a spill directory; a listing that took
	// the directory for a session would put a row with no id in front of the
	// user.
	list, err := session.List(dir, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want the empty bucket's sessions only", list)
	}
}

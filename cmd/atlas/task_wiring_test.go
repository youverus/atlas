package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/session"
	"pi-go/internal/tool"
)

// names is the tool names of a set, in order, for assertions that read better as
// a list than as a loop.
func names(set []tool.Tool) []string {
	out := make([]string, 0, len(set))
	for _, candidate := range set {
		out = append(out, candidate.Name())
	}
	return out
}

func holding(list []string, want string) bool {
	for _, name := range list {
		if name == want {
			return true
		}
	}
	return false
}

// The wiring is what this asserts, not the tools: a set built with a task
// service must offer the three task tools, and bash must gain the argument that
// starts one. Both halves come from the same switch, so a front end that wired
// one and forgot the other would be the version of this bug a user sees.
func TestTheTaskToolsJoinTheSet(t *testing.T) {
	manager, err := session.InMemory(t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	spill := tool.NewSpill(func() string { return t.TempDir() })
	wiring := assembly.TasksFor(manager, spill)
	if wiring.Service == nil {
		t.Skip("this platform has no detached runner")
	}
	if len(wiring.Tools) != 3 {
		t.Fatalf("want three task tools, got %d", len(wiring.Tools))
	}

	set := assembly.ToolSet(&config.Config{}, nil, nil, noGit, nil, wiring)
	list := names(set(t.TempDir()))
	for _, want := range []string{"task_query", "task_output", "task_stop", "bash"} {
		if !holding(list, want) {
			t.Errorf("the set does not hold %s: %v", want, list)
		}
	}

	// bash must carry run_in_background, because a task the model cannot start
	// is a tool it can only read about.
	for _, candidate := range set(t.TempDir()) {
		if candidate.Name() != "bash" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(candidate.Parameters(), &schema); err != nil {
			t.Fatalf("bash schema: %v", err)
		}
		if _, ok := schema.Properties["run_in_background"]; !ok {
			t.Errorf("bash does not offer run_in_background: %s", candidate.Parameters())
		}
	}
}

// Without the service the set must look exactly like it did before tasks
// existed: no tools, and no argument that could only fail.
func TestTheSetWithoutTasks(t *testing.T) {
	set := assembly.ToolSet(&config.Config{}, nil, nil, noGit, nil, assembly.TaskWiring{})
	list := names(set(t.TempDir()))
	for _, unwanted := range []string{"task_query", "task_output", "task_stop"} {
		if holding(list, unwanted) {
			t.Errorf("the set holds %s without a service: %v", unwanted, list)
		}
	}
	for _, candidate := range set(t.TempDir()) {
		if candidate.Name() != "bash" {
			continue
		}
		if strings.Contains(string(candidate.Parameters()), "run_in_background") {
			t.Errorf("bash offers run_in_background without a service: %s", candidate.Parameters())
		}
	}
}

// The record file and the output files belong to the session, so deleting a
// conversation takes its background history with it.
func TestTaskRecordsLiveInTheSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	manager, err := session.InMemory(dir, session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	spill := tool.NewSpill(func() string { return filepath.Join(dir, spillDirName) })
	wiring := assembly.TasksFor(manager, spill)
	if wiring.Service == nil {
		t.Skip("this platform has no detached runner")
	}
	if got, want := wiring.Service.Path(), filepath.Join(manager.Dir(), "tasks.jsonl"); got != want {
		t.Fatalf("record file: got %s, want %s", got, want)
	}
}

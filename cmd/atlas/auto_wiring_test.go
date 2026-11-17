package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/classify"
	"pi-go/internal/permission"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// autoState is a side state whose engine is in auto with a scripted classifier:
// the seam A10 added, from the terminal's side.
func autoState(t *testing.T, answer faux.Response) (*sideState, *faux.Provider) {
	t.Helper()
	script := faux.New(answer)
	engine := permission.New(permission.Options{
		Mode: permission.ModeAuto,
		Classifier: &classify.Classifier{
			Provider: script,
			Model:    wire.Model{ID: "judge", MaxTokens: 4096},
		},
	})
	return newSideStateWith(assembly.NewGate(engine)), script
}

// TestAutoRunsTheAskTheClassifierAllows is the mode end to end: a write in the
// main conversation has no rule, the classifier says allow, and the file is
// written without the user being asked anything.
func TestAutoRunsTheAskTheClassifierAllows(t *testing.T) {
	state, script := autoState(t, faux.Response{
		Text: `{"verdict":"allow","reason":"creating a file in the workspace"}`,
	})
	// An approve that fails the test if it is reached: the classifier answering
	// is the whole point, and a prompt here would mean it did not.
	state.approve = func(tool.Call) bool {
		t.Error("the user was asked about a call the classifier allowed")
		return false
	}

	dir := t.TempDir()
	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "write", json.RawMessage(`{"path":"made.txt","content":"hi"}`)),
		}},
		faux.Response{Text: "written"},
	)
	if _, err := application.Ask(context.Background(), wire.UserMessage("write it"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Fatalf("the classified write did not run: %v", err)
	}
	if calls := script.Calls(); len(calls) != 1 {
		t.Fatalf("the classifier was asked %d times, want 1", len(calls))
	}
}

// TestAutoStopsTheCallTheClassifierRefuses: the other verdict. The file must not
// exist, and the model is told the decision came from the classifier rather than
// from a tool failure, so it stops instead of rewriting the argument.
func TestAutoStopsTheCallTheClassifierRefuses(t *testing.T) {
	state, _ := autoState(t, faux.Response{
		Text: `{"verdict":"deny","reason":"writes outside the workspace"}`,
	})
	// The user would say yes; the classifier is what has to stop it.
	state.approve = func(tool.Call) bool { return true }

	dir := t.TempDir()
	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "write", json.RawMessage(`{"path":"kept.txt","content":"hi"}`)),
		}},
		faux.Response{Text: "stopped"},
	)
	events := &eventLog{}
	if _, err := application.Ask(context.Background(), wire.UserMessage("write it"), events.collect); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept.txt")); !os.IsNotExist(err) {
		t.Fatalf("the write ran despite the verdict: %v", err)
	}
	reason := refusedReason(t, events.events, "c1")
	if !strings.Contains(reason, "auto classifier") {
		t.Fatalf("the model was told %q, want the classifier's refusal", reason)
	}
}

// eventLog is the sink for the two tests above; refusedReason wants a slice.
type eventLog struct{ events []wire.AgentEvent }

func (l *eventLog) collect(event wire.AgentEvent) error {
	l.events = append(l.events, event)
	return nil
}

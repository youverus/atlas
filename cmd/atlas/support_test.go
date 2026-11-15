package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// cancelled is a provider whose context is already done, which is what a turn
// interrupted by Ctrl-C looks like to the kernel.
type cancelled struct{}

func (cancelled) API() string { return "faux" }

func (cancelled) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	return nil, ctx.Err()
}

func inMemory(t *testing.T) *session.Manager {
	t.Helper()
	manager, err := session.InMemory(t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return manager
}

// TestAskKeepsTheSessionWhenATurnIsCancelled pins the difference between an
// interrupted turn and a failed one: the first is how Ctrl-C is supposed to end
// and the session continues, the second is reported.
func TestAskKeepsTheSessionWhenATurnIsCancelled(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	application, err := app.New(app.Config{
		Model:    wire.Model{ID: "m1", API: "faux", Provider: "test"},
		Provider: cancelled{},
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ask(ctx, application, "hi", newRenderer(out, errOut, false)); err != nil {
		t.Fatalf("ask: %v, want nil for a cancelled turn", err)
	}
	if !strings.Contains(errOut.String(), "aborted") {
		t.Fatalf("stderr = %q, want an aborted summary", errOut.String())
	}
}

func TestQuitIsNotACommand(t *testing.T) {
	done, err := replCommand(context.Background(), nil, nil, "/quit", models{}, nil, nil, emptyPrompts(), io.Discard, nil, mcpState{}, nil, nil)
	if done {
		t.Fatal("done = true, want /quit to stop being an exit")
	}
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v, want an unknown-command error", err)
	}

	if done, err := replCommand(context.Background(), nil, nil, "/exit", models{}, nil, nil, emptyPrompts(), io.Discard, nil, mcpState{}, nil, nil); err != nil || !done {
		t.Fatalf("replCommand(/exit, mcpState{}, nil) = (%v, %v), want the exit", done, err)
	}
}

// TestHelpTextMatchesTheCommandTable pins the shape of /help: one line per
// command, the summaries in one column. The column is measured from the table,
// so the test measures it too instead of freezing a width that the next usage
// string would break.
func TestHelpTextMatchesTheCommandTable(t *testing.T) {
	text := helpText()
	for _, c := range commands {
		if !strings.Contains(text, c.name) {
			t.Fatalf("help text is missing %s:\n%s", c.name, text)
		}
	}

	// The table comes first: one row per command plus the catch-all. The blank
	// line and the key block below it are not rows, so they are cut off before
	// the column is measured.
	table, keys, found := strings.Cut(strings.TrimSuffix(text, "\n"), "\n\n")
	if !found || !strings.HasPrefix(keys, "keys: ") {
		t.Fatalf("help text = %q, want a table then a keys block", text)
	}
	lines := strings.Split(table, "\n")
	if len(lines) != len(commands)+1 {
		t.Fatalf("help table has %d lines, want one per command plus the catch-all:\n%s", len(lines), text)
	}
	column := -1
	for i, line := range lines {
		summary := "sent to the model"
		if i < len(commands) {
			summary = commands[i].summary
		}
		// The summary is separated by two spaces, so its offset is the last
		// place the summary can start: anything left of that is the label and
		// its padding.
		offset := strings.LastIndex(line, summary)
		if offset < 0 {
			t.Fatalf("line %d = %q, want the summary %q", i, line, summary)
		}
		if column < 0 {
			column = offset
		} else if offset != column {
			t.Fatalf("line %d = %q, wants its summary at column %d like the rest (%d)", i, line, column, offset)
		}
	}
}

// emptyPrompts is a source that reads nothing.
//
// The commands that do not ask about the prompt do not need a real one, and a
// source pointed at the working directory would pick up whatever file happens
// to be there — the machine's, not the test's.
func emptyPrompts() *promptSource {
	return &promptSource{builder: promptBuilder{noContext: true, noSkills: true}}
}

// noGit is the git family with nothing in it. The real one is built from the
// machine's PATH, and a test that varied with whether git is installed would be
// asserting about the machine rather than about the code.
func noGit(string) []tool.Tool { return nil }

// fixedTools is a set that does not change, which is what an MCP list looks like
// to the tests that only care about the rest of workspaceTools.
func fixedTools(set []tool.Tool) func() []tool.Tool {
	return func() []tool.Tool { return set }
}

// workspaceTools is toolSet asked for one directory. The tests describe a
// workspace and what it holds; a factory is what the app needs, and asking it
// here keeps every assertion about the same function production uses.
func workspaceTools(cwd string, webSet, mcpSet []tool.Tool, block *config.Sandbox) []tool.Tool {
	return toolSet(&config.Config{Sandbox: block}, webSet, fixedTools(mcpSet), noGit, nil, taskWiring{})(cwd)
}

// workspaceToolsWithSpill is workspaceTools with a destination for output too
// large for a message, which is what a real session always has.
func workspaceToolsWithSpill(cwd string, spill *tool.Spill) []tool.Tool {
	return toolSet(&config.Config{}, nil, nil, noGit, spill, taskWiring{})(cwd)
}

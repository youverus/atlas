package app

// The kernel half of A11: the tool set is asked again before every turn, because
// an MCP server can declare a tool while the conversation runs.

import (
	"context"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// lateTool is the tool that was not there at the start: an MCP server declaring
// one mid-conversation, reduced to the one thing this test is about, its name.
type lateTool struct{ echoTool }

func (lateTool) Name() string        { return "late" }
func (lateTool) Label() string       { return "Late" }
func (lateTool) Description() string { return "Declared after the first turn." }

// names joins the registry's names for a failure message.
func registryNames(application *App) string {
	return strings.Join(application.ag.Registry().Names(), ",")
}

// TestAToolThatAppearsBetweenTurnsIsCallableOnTheNextOne covers the promise the
// completion criterion makes: a tool a server added while the session ran is on
// the next request, without a restart and without a new agent.
func TestAToolThatAppearsBetweenTurnsIsCallableOnTheNextOne(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	asked := 0
	grow := func(string) []tool.Tool {
		asked++
		if asked == 1 {
			return []tool.Tool{echoTool{}}
		}
		return []tool.Tool{echoTool{}, lateTool{}}
	}
	application := newTestApp(t, manager, prov, func(cfg *Config) { cfg.Tools = grow })

	if got := registryNames(application); got != "echo" {
		t.Fatalf("tools = %q, want the one the session started with", got)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := registryNames(application); got != "echo,late" {
		t.Fatalf("tools = %q, want the tool that appeared to be there for the next turn", got)
	}
	if _, ok := application.ag.Registry().Get("late"); !ok {
		t.Fatal("the new tool is declared but cannot be dispatched")
	}
}

// TestAToolThatDisappearsStopsBeingCallable is the same rule in the other
// direction: a server that withdrew a tool must not leave a name the model can
// call and that nothing answers.
func TestAToolThatDisappearsStopsBeingCallable(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	asked := 0
	shrink := func(string) []tool.Tool {
		asked++
		if asked == 1 {
			return []tool.Tool{echoTool{}, lateTool{}}
		}
		return []tool.Tool{lateTool{}}
	}
	application := newTestApp(t, manager, prov, func(cfg *Config) { cfg.Tools = shrink })

	if got := registryNames(application); got != "echo,late" {
		t.Fatalf("tools = %q, want both to start with", got)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := registryNames(application); got != "late" {
		t.Fatalf("tools = %q, want the withdrawn one gone", got)
	}
	if _, ok := application.ag.Registry().Get("echo"); ok {
		t.Fatal("a tool the set no longer offers is still dispatchable")
	}
}

// TestAToolWithoutASetIsNotABug pins the nil case: a kernel handed no tools asks
// nothing and declares nothing, and a turn still runs.
func TestAToolWithoutASetIsNotABug(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "answer"}))

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := registryNames(application); got != "" {
		t.Fatalf("tools = %q, want none", got)
	}
}

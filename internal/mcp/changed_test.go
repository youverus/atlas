package mcp

// The tests of section 5: a tool list that changes while the session runs, and a
// per-server whitelist that narrows it. Both are about the same thing — the tool
// table is not assembled once and put away.

import (
	"context"
	"strings"
	"testing"
	"time"

	"pi-go/internal/tool"
)

// waitUntil polls one server until a predicate holds, and fails with what it saw
// last. A refresh runs on a goroutine of its own, so the test waits for the
// outcome rather than for the notification that caused it.
func waitUntil(t *testing.T, set *Set, want string, holds func(*Server) bool) *Server {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		servers := set.Servers()
		if len(servers) == 1 {
			if holds(servers[0]) {
				return servers[0]
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; tools = %v, declared = %d, refresh error = %v",
					want, wireNames(servers[0]), servers[0].Declared, servers[0].RefreshErr)
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s with %d servers", want, len(servers))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wireNames is what a failure message needs: the names the model would see.
func wireNames(server *Server) []string {
	names := make([]string, 0, len(server.Tools))
	for _, remote := range server.Tools {
		names = append(names, remote.WireName)
	}
	return names
}

// execute calls one tool of the set by its model-facing name.
func execute(t *testing.T, set *Set, name string) {
	t.Helper()
	for _, candidate := range set.Tools() {
		if candidate.Name() != name {
			continue
		}
		if _, err := candidate.Execute(context.Background(), tool.Call{Name: name}, nil); err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		return
	}
	t.Fatalf("no tool named %q", name)
}

// TestAToolDeclaredMidSessionJoinsTheSession is the rule section 5 exists for: a
// server that adds a tool while the conversation runs has it on the next listing,
// without a restart and without the model having to be told.
func TestAToolDeclaredMidSessionJoinsTheSession(t *testing.T) {
	set := connectFake(t, "grow")
	if got := len(set.Servers()[0].Tools); got != 2 {
		t.Fatalf("tools = %d, want the 2 of the first listing", got)
	}

	execute(t, set, "mcp__fake__add_tool")

	server := waitUntil(t, set, "the third tool", func(s *Server) bool { return len(s.Tools) == 3 })
	if got := server.Tools[2].WireName; got != "mcp__fake__fresh_tool" {
		t.Fatalf("new tool = %q, want the one the server added", got)
	}
	if got := server.Declared; got != 3 {
		t.Fatalf("declared = %d, want 3", got)
	}
	// The point of the exercise: the next turn asks the set for its tools.
	if got := len(set.Tools()); got != 3 {
		t.Fatalf("tools for the model = %d, want 3", got)
	}
}

// TestAChangeThatArrivesBeforeTheHookIsNotLost covers the window between the first
// listing and the hook that listens for later ones. A server can answer with a
// notification before the client has anywhere to put it, and dropping it would
// make the tool list wrong until the next restart.
func TestAChangeThatArrivesBeforeTheHookIsNotLost(t *testing.T) {
	set := connectFake(t, "eager")
	// watch lists again synchronously, so the set is already complete here.
	if got := len(set.Servers()[0].Tools); got != 3 {
		t.Fatalf("tools = %d, want the notification from the handshake to be acted on", got)
	}
}

// TestARefreshKeepsTheWhitelist checks that narrowing survives a listing the
// session did not ask for: the filter belongs to the configuration, not to the
// first answer.
func TestARefreshKeepsTheWhitelist(t *testing.T) {
	spec := fakeSpec(t, "grow")
	spec.Tools = []string{"add_tool"}
	set := Open(context.Background(), []Spec{spec}, Identity{Name: "atlas", Version: "0.0.0"})
	t.Cleanup(set.Close)
	if got := len(set.Tools()); got != 1 {
		t.Fatalf("tools = %d, want the one the whitelist allows", got)
	}

	execute(t, set, "mcp__fake__add_tool")

	server := waitUntil(t, set, "the server to declare three", func(s *Server) bool { return s.Declared == 3 })
	if got := len(server.Tools); got != 1 {
		t.Fatalf("tools = %d after the refresh, want the whitelist to still hold", got)
	}
	if names := wireNames(server); names[0] != "mcp__fake__add_tool" {
		t.Fatalf("tools = %v, want the allowed one", names)
	}
}

// TestAWhitelistNarrowsTheToolList is the second half of section 5: two numbers,
// because "2" alone cannot say whether the server has two tools or the session was
// allowed two of fifty.
func TestAWhitelistNarrowsTheToolList(t *testing.T) {
	spec := fakeSpec(t, "plain")
	spec.Tools = []string{"list_dir"}
	set := Open(context.Background(), []Spec{spec}, Identity{Name: "atlas", Version: "0.0.0"})
	t.Cleanup(set.Close)

	server := set.Servers()[0]
	if got := len(server.Tools); got != 1 {
		t.Fatalf("tools = %d, want the one name asked for", got)
	}
	if got := server.Tools[0].WireName; got != "mcp__fake__list_dir" {
		t.Fatalf("tool = %q, want the allowed one to keep its name", got)
	}
	if got := server.Declared; got != 2 {
		t.Fatalf("declared = %d, want both tools the server offers", got)
	}
	if got := server.Missing; len(got) != 0 {
		t.Fatalf("missing = %v, want none", got)
	}
	if notes := set.Notes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want none: a whitelist that works is not a warning", notes)
	}
}

// TestAWhitelistThatNamesNothingIsReported is the typo the whitelist invites. The
// tool simply not appearing would send the reader looking at the server.
func TestAWhitelistThatNamesNothingIsReported(t *testing.T) {
	spec := fakeSpec(t, "plain")
	spec.Tools = []string{"list_dir", "read_flie"}
	set := Open(context.Background(), []Spec{spec}, Identity{Name: "atlas", Version: "0.0.0"})
	t.Cleanup(set.Close)

	if got := set.Servers()[0].Missing; len(got) != 1 || got[0] != "read_flie" {
		t.Fatalf("missing = %v, want the name the server does not declare", got)
	}
	notes := set.Notes()
	if len(notes) != 1 || !strings.Contains(notes[0], "read_flie") {
		t.Fatalf("notes = %v, want one naming the tool", notes)
	}
}

// TestAFailedRefreshKeepsTheToolsItHad is the failure path: the server says its
// list changed and then cannot answer. The tools in hand keep working, and the
// reason is kept for /mcp rather than thrown away.
func TestAFailedRefreshKeepsTheToolsItHad(t *testing.T) {
	set := connectFake(t, "failrefresh")
	if got := len(set.Tools()); got != 2 {
		t.Fatalf("tools = %d, want the first listing", got)
	}

	execute(t, set, "mcp__fake__read_file")

	server := waitUntil(t, set, "the failed refresh", func(s *Server) bool { return s.RefreshErr != nil })
	if got := len(server.Tools); got != 2 {
		t.Fatalf("tools = %d, want the list from before the notification", got)
	}
	if !strings.Contains(server.RefreshErr.Error(), "tools/list") {
		t.Fatalf("refresh error = %v, want the server's reason", server.RefreshErr)
	}
}

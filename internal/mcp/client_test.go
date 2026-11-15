package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"pi-go/internal/tool"
)

// TestHandshakeAcceptsKnownVersions covers the two halves of "pin one
// generation, not one string": the current revision and an older one both run,
// and the client records what the server picked.
func TestHandshakeAcceptsKnownVersions(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		want     string
	}{
		{"basic", protocolVersion},
		{"old", "2025-06-18"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			set := connectFake(t, tc.scenario)
			server := set.Servers()[0]
			if got := server.Version; got != "9.9.9" {
				t.Fatalf("server version = %q, want the one the handshake reported", got)
			}
			if len(server.Tools) != 2 {
				t.Fatalf("tools = %d, want the 2 the server listed", len(server.Tools))
			}
			if got := set.servers[0].session.version; got != tc.want {
				t.Fatalf("negotiated version = %q, want %q", got, tc.want)
			}
			// The initialized notification is what tells the server the client is
			// ready; a handshake without it leaves some servers waiting.
			joined := strings.Join(fakeLog(t), ",")
			if !strings.Contains(joined, "notifications/initialized") {
				t.Fatalf("requests = %s, want notifications/initialized", joined)
			}
		})
	}
}

// TestHandshakeRefusesUnknownVersion is the other half: a revision outside the
// list is refused, and the message names both the server's version and ours so
// the reader can tell which side to change.
func TestHandshakeRefusesUnknownVersion(t *testing.T) {
	set := Open(context.Background(), []Spec{fakeSpec(t, "alien")}, Identity{Name: "atlas", Version: "0.7.0"})
	t.Cleanup(set.Close)

	server := set.Servers()[0]
	if server.Err == nil {
		t.Fatal("connect succeeded, want a refusal")
	}
	for _, want := range []string{"1999-01-01", protocolVersion} {
		if !strings.Contains(server.Err.Error(), want) {
			t.Fatalf("error = %v, want it to name %q", server.Err, want)
		}
	}
	if len(set.Tools()) != 0 {
		t.Fatalf("tools = %d, want none from a refused server", len(set.Tools()))
	}
}

// TestStdioFraming runs the handshake and a list over a pipe that carries a
// carriage return, empty lines and a CRLF terminator.
func TestStdioFraming(t *testing.T) {
	set := connectFake(t, "framing")
	if got := len(set.Servers()[0].Tools); got != 2 {
		t.Fatalf("tools = %d, want 2 parsed out of the noise", got)
	}
}

// TestStdioIncompleteMessage is the crash case: half a message and no newline.
// Reporting it as a truncated message is the point — the alternative is a client
// that says the server answered nothing.
func TestStdioIncompleteMessage(t *testing.T) {
	set := Open(context.Background(), []Spec{fakeSpec(t, "incomplete")}, Identity{Name: "atlas", Version: "0.7.0"})
	t.Cleanup(set.Close)

	server := set.Servers()[0]
	if server.Err == nil {
		t.Fatal("connect succeeded, want a transport failure")
	}
	if !strings.Contains(server.Err.Error(), errIncompleteMessage.Error()) {
		t.Fatalf("error = %v, want it to name %q", server.Err, errIncompleteMessage)
	}
}

// TestToolsListFollowsCursor proves the cursor loop reads every page. A client
// that stops at the first page loses tools silently, which looks exactly like a
// server that never declared them.
func TestToolsListFollowsCursor(t *testing.T) {
	set := connectFake(t, "paged")
	tools := set.Servers()[0].Tools
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want both pages", len(tools))
	}
	if tools[0].Name != "first" || tools[1].Name != "second" {
		t.Fatalf("tools = %v, want the pages in order", []string{tools[0].Name, tools[1].Name})
	}
}

// TestToolNamesAreNormalized pins the spelling every other client in this
// ecosystem uses: a name has to survive a provider's pattern and stay identical
// between clients, or the same server becomes two different tools.
func TestToolNamesAreNormalized(t *testing.T) {
	servers := []*Server{
		{Name: "my.server", Tools: []RemoteTool{{Server: "my.server", Name: "read file"}}},
		{Name: "a.b", Tools: []RemoteTool{{Server: "a.b", Name: "x"}}},
		{Name: "a_b", Tools: []RemoteTool{{Server: "a_b", Name: "x"}}},
	}
	assignNames(servers)

	if got := servers[0].Tools[0].WireName; got != "mcp__my_server__read_file" {
		t.Fatalf("name = %q, want mcp__my_server__read_file", got)
	}
	// "a.b" and "a_b" normalize onto the same prefix, so one of the two has to
	// get the hash suffix. Two tools under one name would be worse than a long
	// one, and the hash makes the choice independent of order.
	left, right := servers[1].Tools[0].WireName, servers[2].Tools[0].WireName
	if left == right {
		t.Fatalf("both named %q, want distinct names", left)
	}
	for _, name := range []string{left, right} {
		if len(name) > toolNameLimit {
			t.Fatalf("name %q is longer than %d", name, toolNameLimit)
		}
	}
}

// TestCallResultMapping runs one tools/call whose answer uses every block type
// this client maps, and checks the four things the model receives: the text, the
// image, the spilled audio, and the server's own failure flag.
func TestCallResultMapping(t *testing.T) {
	set := connectFake(t, "basic")
	proxy := findTool(t, set, "mcp__fake__read_file")

	result, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"path":"/"}`)}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("IsError = false, want the server's isError flag")
	}
	if got := string(result.StructuredContent); got != `{"rows":3}` {
		t.Fatalf("structured content = %s, want it passed through", got)
	}

	var text string
	var image bool
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			text += block.Text
		case "image":
			image = true
			if block.MIMEType != "image/png" {
				t.Fatalf("image mime = %q, want image/png", block.MIMEType)
			}
		}
	}
	if !image {
		t.Fatal("no image block, want the image content passed through")
	}
	for _, want := range []string{"the answer", "atlas cannot show it inline", "the report", "no tool that reads MCP resources"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text = %q, want it to contain %q", text, want)
		}
	}
	// The audio block is only useful if the path in the message is real.
	saved := text[strings.Index(text, "saved to ")+len("saved to "):]
	saved = strings.TrimSuffix(strings.Fields(saved)[0], ";")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("audio path %q: %v", saved, err)
	}
}

// TestCallsAreNotRetried holds the rule that matters most about a tool call: a
// call that failed may have run, so it is sent once. The fake server counts the
// requests, which is the only way the client's silence becomes observable.
func TestCallsAreNotRetried(t *testing.T) {
	set := connectFake(t, "crashtool")
	proxy := findTool(t, set, "mcp__fake__read_file")

	_, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, nil)
	if err == nil {
		t.Fatal("call succeeded, want the server's error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want the server's message", err)
	}

	calls := 0
	for _, method := range fakeLog(t) {
		if method == "tools/call" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("tools/call requests = %d, want exactly 1", calls)
	}
}

// TestUnreachableServerWarnsOnce is the startup promise: one line, naming the
// server and why, instead of a session that quietly has no tools.
func TestUnreachableServerWarnsOnce(t *testing.T) {
	set := Open(context.Background(), []Spec{fakeSpec(t, "failed")}, Identity{Name: "atlas", Version: "0.7.0"})
	t.Cleanup(set.Close)

	notes := set.Notes()
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want exactly one", notes)
	}
	for _, want := range []string{"fake", "the server needs a token"} {
		if !strings.Contains(notes[0], want) {
			t.Fatalf("note = %q, want it to contain %q", notes[0], want)
		}
	}
	if len(set.Tools()) != 0 {
		t.Fatalf("tools = %d, want none from a dead server", len(set.Tools()))
	}
}

// findTool picks one tool out of a set by wire name.
func findTool(t *testing.T, set *Set, name string) tool.Tool {
	t.Helper()
	for _, candidate := range set.Tools() {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("no tool named %q", name)
	return nil
}

// TestToolDeclaration is what the model sees: the name the assignment step gave
// it, the server's schema unchanged, and a description that still says where the
// tool came from when the server wrote none.
func TestToolDeclaration(t *testing.T) {
	remote := RemoteTool{
		Server: "docs",
		Name:   "search",
		Schema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}
	remote.WireName = "mcp__docs__search"

	declaration := tool.Declaration(&toolProxy{remote: remote})
	if declaration.Name != "mcp__docs__search" {
		t.Fatalf("name = %q, want the wire name", declaration.Name)
	}
	if string(declaration.Parameters) != string(remote.Schema) {
		t.Fatalf("parameters = %s, want the server's schema unchanged", declaration.Parameters)
	}
	if !strings.Contains(declaration.Description, "docs") {
		t.Fatalf("description = %q, want it to name the server", declaration.Description)
	}

	empty := tool.Declaration(&toolProxy{remote: RemoteTool{Server: "docs", Name: "bare", WireName: "mcp__docs__bare"}})
	if string(empty.Parameters) != emptySchema {
		t.Fatalf("parameters = %s, want %s", empty.Parameters, emptySchema)
	}
}

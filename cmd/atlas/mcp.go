package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"pi-go/internal/config"
	"pi-go/internal/mcp"
	"pi-go/internal/tool"
)

// mcpState is the MCP half of a session: the connections, where they were
// configured, and where an authorization would be kept.
//
// The zero value is safe and is what every caller with nothing to connect to
// passes, which matters because the front ends carry this by value through four
// layers of call signatures. path is kept even when there are no servers: "there
// are none" is not actionable without saying where they would go. specs are kept
// for /mcp login, which has to reach a server this session did not connect to —
// the one that needs a login is by definition not connected.
type mcpState struct {
	set   *mcp.Set
	path  string
	store *mcp.OAuthStore
	specs []mcp.Spec
}

// mcpFor connects to the servers the configuration names.
//
// A broken config file is an error and a server that fails to connect is a
// warning, which is the split webTools makes: the file is something the user
// wrote and can fix, while one unreachable server is a fact about this machine
// and only costs its own tools. The warning carries the failure as the client
// worded it, including the server's stderr tail, because "connection closed" on
// its own sends the reader to the wrong place.
func mcpFor(ctx context.Context, cwd string, spill *tool.Spill) (mcpState, []string, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return mcpState{}, nil, err
	}
	cfg, err := config.LoadMCP(home, cwd)
	if err != nil {
		return mcpState{}, nil, err
	}

	// One store for the whole session, opened whether or not a server needs it:
	// /mcp login reads and writes it, and it never fails to open.
	store := mcp.NewOAuthStore(filepath.Join(home, mcp.OAuthFileName))
	state := mcpState{path: cfg.Global, store: store}
	// The notes are what the configuration left out: a plugin that did not
	// load, a plugin server whose name was taken. They come first, because a
	// reader who sees an unexpected server list wants the reason before the
	// list of failures.
	notes := append([]string(nil), cfg.Notes...)
	for _, name := range cfg.Missing {
		notes = append(notes, fmt.Sprintf("mcp: ${%s} is not set, so a server in %s starts with the literal value", name, cfg.Global))
	}
	if len(cfg.Servers) == 0 {
		return state, notes, nil
	}

	specs := make([]mcp.Spec, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		spec := mcp.Spec{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
			Dir:     server.Cwd,
			URL:     server.URL,
			Headers: server.Headers,
			Timeout: time.Duration(server.Timeout) * time.Second,
			Tools:   server.Tools,
		}
		if server.Auth == "oauth" {
			spec.OAuth = &mcp.OAuthSpec{Scopes: server.Scopes, Store: store}
		}
		specs = append(specs, spec)
	}
	state.specs = specs
	// The identity is this binary's: a server's log saying "atlas 0.7.0" is the
	// only way to tell which client an entry was tested with.
	state.set = mcp.Open(ctx, specs, mcp.Identity{Name: "atlas", Version: version})
	// A result too large for a message is written next to the session, same as
	// the shell's output: both are "here is the part you could not see", and two
	// destinations for one idea would be two things to clean up.
	state.set.Spill = spill
	return state, append(notes, state.set.Notes()...), nil
}

// tools returns the remote tools as the model's own, as they are right now. Nil
// receivers are fine, because a session without MCP is the normal case.
//
// It is asked again before every turn rather than once at startup: a server can
// declare a tool while the session runs, and the client answers that by listing
// again (docs/mcp.md section 5).
func (s mcpState) tools() []tool.Tool { return s.set.Tools() }

// close ends every connection. It is deferred at startup, so a session that
// never connected anything pays nothing.
func (s mcpState) close() { s.set.Close() }

// report is /mcp. With no argument it lists the servers; with one it lists that
// server's tools; with "login" it starts an authorization.
//
// The command was read-only, and login is a deliberate exception: "needs login" is
// a state whose only useful next step is that verb, and a command that can
// describe a state without leaving it sends the reader looking for a second
// document (docs/mcp-oauth.md section 4.3). Every other verb stays out.
func (s mcpState) report(ctx context.Context, args []string, out io.Writer) error {
	if isMCPLogin(args) {
		return s.login(ctx, args[1:], out)
	}
	servers := s.set.Servers()
	if len(args) > 0 {
		name := strings.Join(args, " ")
		for _, server := range servers {
			if server.Name == name {
				return writeMCPTools(out, server)
			}
		}
		return fmt.Errorf("/mcp: no server named %q; /mcp lists the configured ones", name)
	}
	if len(servers) == 0 {
		fmt.Fprintf(out, "no MCP servers are configured; they live under \"mcpServers\" in %s\n", s.path)
		return nil
	}
	return writeMCPServers(out, servers)
}

// isMCPLogin reports whether the argument list asks for a login. It exists so that
// the two states that must refuse one (a side conversation, an archived session)
// can recognise it without repeating the shape of the argument.
func isMCPLogin(args []string) bool {
	return len(args) > 0 && args[0] == "login"
}

// login starts the authorization flow for one server.
//
// The wait is measured in minutes and ends when a browser comes back, so it runs in
// its own goroutine and the command returns at once. Waiting here would freeze the
// interface for the whole of it, Ctrl+C included, because runTUICommand is
// synchronous (docs/mcp-oauth.md section 4.3). The report is written to out, which
// is the writer the command was already answering on: the interface turns each
// line into a notice, so a line that arrives later still lands in the transcript.
func (s mcpState) login(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("/mcp login takes one server name; /mcp lists the configured ones")
	}
	name := args[0]
	spec, ok := s.spec(name)
	if !ok {
		return fmt.Errorf("/mcp: no server named %q; /mcp lists the configured ones", name)
	}
	if spec.OAuth == nil {
		return fmt.Errorf("server %q has no \"auth\": \"oauth\" in %s, so there is nothing to log in to", name, s.path)
	}
	// Written before the goroutine starts, because both write to the same
	// writer: the interface turns each line into a notice, and a notice that
	// arrived before the sentence introducing it would read as a mistake.
	fmt.Fprintf(out, "logging in to %s; the URL to open comes next\n", name)
	go func() {
		// The turn this command ran in is over the moment report returns: the
		// interface cancels a turn's context as soon as its Run ends
		// (internal/tui/model.go), which would kill the wait before the browser
		// could answer. Detaching is the honest spelling of the decision in
		// docs/mcp-oauth.md section 4.3 — this version cannot cancel the wait, so
		// the wait must not inherit a cancellation that always fires. Login
		// applies its own five-minute timeout, which is what bounds it.
		result, err := mcp.Login(context.WithoutCancel(ctx), spec, func(authorize, redirectURI string) {
			fmt.Fprintf(out, "open this URL to authorize %s:\n\n  %s\n\nwaiting for the callback on %s\n", name, authorize, redirectURI)
		})
		if err != nil {
			fmt.Fprintf(out, "/mcp login %s failed: %v\n", name, err)
			return
		}
		fmt.Fprintf(out, "logged in to %s", name)
		if result.Scopes != "" {
			fmt.Fprintf(out, " (scopes: %s)", result.Scopes)
		}
		// A login does not add tools to a session that was built before it: the
		// tool list is fixed when the session starts. Saying so here is the only
		// place the reader finds out (docs/mcp-oauth.md section 6).
		fmt.Fprint(out, "\nits tools appear after you restart atlas\n")
	}()
	return nil
}

// spec returns the connection spec of a configured server. /mcp login needs it for
// a server this session never connected to, which is the one that needs a login.
func (s mcpState) spec(name string) (mcp.Spec, bool) {
	for _, spec := range s.specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return mcp.Spec{}, false
}

// writeMCPServers is the table /mcp prints. It is separate from report so the
// rendering can be tested against servers built by hand, without a process
// speaking JSON-RPC.
func writeMCPServers(out io.Writer, servers []*mcp.Server) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SERVER\tVERSION\tTOOLS\tSTATE")
	for _, server := range servers {
		version := server.Version
		if version == "" {
			version = "-"
		}
		state := "connected"
		switch {
		case server.NeedsLogin:
			state = "needs login"
		case server.Err != nil:
			state = "failed"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", server.Name, version, toolCount(server), state)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	// The failures are printed under the table rather than in it: an error is as
	// long as it needs to be, and a row that stretched the columns would push the
	// columns the reader came for off the screen. A server that needs a login is
	// here for the same reason: the state column has room for two words, not for
	// the command that changes them.
	for _, server := range servers {
		switch {
		case server.NeedsLogin:
			fmt.Fprintf(out, "\n%s: not connected; \"/mcp login %s\" authorizes it\n", server.Name, server.Name)
		case server.Err != nil:
			fmt.Fprintf(out, "\n%s: %v\n", server.Name, server.Err)
		case server.RefreshErr != nil:
			// The server is connected and its old tools still work, so this is
			// a note rather than a state: the count in the table is the list it
			// gave last, not the one it says it has now.
			fmt.Fprintf(out, "\n%s: its tool list changed and could not be read again, so the count is the one from before: %v\n", server.Name, server.RefreshErr)
		}
	}
	fmt.Fprintf(out, "\n/mcp <server> lists that server's tools\n")
	return nil
}

// toolCount is the TOOLS column. A server narrowed by its "tools" list shows
// both numbers, because "2" on its own cannot tell "this server has two tools"
// from "this session was allowed two of fifty".
func toolCount(server *mcp.Server) string {
	if server.Declared == len(server.Tools) {
		return fmt.Sprintf("%d", len(server.Tools))
	}
	return fmt.Sprintf("%d of %d", len(server.Tools), server.Declared)
}

// writeMCPTools prints one server's tools, which is the question a reader asks
// after "did it connect" and before "why is the model calling the wrong one".
func writeMCPTools(out io.Writer, server *mcp.Server) error {
	if server.NeedsLogin {
		fmt.Fprintf(out, "%s: not connected; \"/mcp login %s\" authorizes it\n", server.Name, server.Name)
		return nil
	}
	if server.Err != nil {
		fmt.Fprintf(out, "%s: %v\n", server.Name, server.Err)
		return nil
	}
	if server.Version == "" {
		fmt.Fprintf(out, "%s: %s tools\n", server.Name, toolCount(server))
	} else {
		fmt.Fprintf(out, "%s %s: %s tools\n", server.Name, server.Version, toolCount(server))
	}
	if len(server.Missing) > 0 {
		// The whitelist is the reader's, so the reader is the one who can fix
		// it. Naming the tools is the whole message.
		fmt.Fprintf(out, "the tools list in mcp.json asks for %s, which this server does not declare\n", quotedList(server.Missing))
	}
	if server.RefreshErr != nil {
		fmt.Fprintf(out, "its tool list changed and could not be read again, so this is the list from before: %v\n", server.RefreshErr)
	}
	if len(server.Tools) == 0 {
		return nil
	}

	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "TOOL\tDECLARED AS\tDESCRIPTION")
	for _, remote := range server.Tools {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", remote.Name, remote.WireName, oneLine(remote.Description, 80))
	}
	return writer.Flush()
}

// quotedList formats names for one line of prose: "a", "b" and "c".
func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = fmt.Sprintf("%q", name)
	}
	if len(quoted) < 2 {
		return strings.Join(quoted, "")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// oneLine folds a description to the first line and cuts it, so one verbose tool
// cannot turn a table into a paragraph.
func oneLine(text string, limit int) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = strings.TrimSpace(text[:index])
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

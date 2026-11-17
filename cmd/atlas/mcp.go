package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"pi-go/internal/assembly"
	"pi-go/internal/mcp"
)

// mcpState is the MCP half of a session as the terminal spells it: the
// connections the kernel opened, plus what /mcp needs to describe them and
// /mcp login needs to start an authorization.
//
// The connections themselves are the assembly's, because a window connects the
// same servers from the same file. This type exists only so the two commands
// below can be methods: a method needs a type of its own, and neither command
// belongs in a package that a window also imports. The zero value is safe — the
// embedded pointer's methods are nil-tolerant — and it is what every caller with
// nothing to connect to passes through four layers of call signatures.
type mcpState struct {
	*assembly.MCPServers
}

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
	servers := s.Servers()
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
		fmt.Fprintf(out, "no MCP servers are configured; they live under \"mcpServers\" in %s\n", s.Path())
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
		return fmt.Errorf("server %q has no \"auth\": \"oauth\" in %s, so there is nothing to log in to", name, s.Path())
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
	for _, spec := range s.Specs() {
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

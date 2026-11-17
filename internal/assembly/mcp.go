package assembly

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"pi-go/internal/config"
	"pi-go/internal/mcp"
	"pi-go/internal/tool"
)

// MCPServers is the MCP half of a session: the connections, where they were
// configured, and where an authorization would be kept.
//
// The zero value is safe and is what every caller with nothing to connect to
// passes, which matters because the front ends carry this by value through four
// layers of call signatures. path is kept even when there are no servers: "there
// are none" is not actionable without saying where they would go. specs are kept
// for /mcp login, which has to reach a server this session did not connect to —
// the one that needs a login is by definition not connected.
type MCPServers struct {
	set   *mcp.Set
	path  string
	store *mcp.OAuthStore
	specs []mcp.Spec
}

// NewMCP connects to the servers the configuration names.
//
// A broken config file is an error and a server that fails to connect is a
// warning, which is the split webTools makes: the file is something the user
// wrote and can fix, while one unreachable server is a fact about this machine
// and only costs its own tools. The warning carries the failure as the client
// worded it, including the server's stderr tail, because "connection closed" on
// its own sends the reader to the wrong place.
func NewMCP(ctx context.Context, cwd string, spill *tool.Spill, version string) (MCPServers, []string, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return MCPServers{}, nil, err
	}
	cfg, err := config.LoadMCP(home, cwd)
	if err != nil {
		return MCPServers{}, nil, err
	}

	// One store for the whole session, opened whether or not a server needs it:
	// /mcp login reads and writes it, and it never fails to open.
	store := mcp.NewOAuthStore(filepath.Join(home, mcp.OAuthFileName))
	state := MCPServers{path: cfg.Global, store: store}
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

// Tools returns the remote tools as the model's own, as they are right now. Nil
// receivers are fine, because a session without MCP is the normal case.
//
// It is asked again before every turn rather than once at startup: a server can
// declare a tool while the session runs, and the client answers that by listing
// again (docs/mcp.md section 5).
func (s *MCPServers) Tools() []tool.Tool {
	if s == nil || s.set == nil {
		return nil
	}
	return s.set.Tools()
}

// Servers are the connections as a front end describes them: name, version,
// state and the tools each one answered with.
func (s *MCPServers) Servers() []*mcp.Server {
	if s == nil || s.set == nil {
		return nil
	}
	return s.set.Servers()
}

// Close ends every connection. It is deferred at startup, so a session that
// never connected anything pays nothing.
func (s *MCPServers) Close() {
	if s == nil || s.set == nil {
		return
	}
	s.set.Close()
}

// MCPDescription is the value for a front end that has a file to name and servers
// to describe but nothing connected. /mcp prints where servers would go even on a
// machine with none, and its login reaches a server through the declarations
// rather than through a connection — the one that needs a login is by definition
// the one that is not connected.
func MCPDescription(path string, specs []mcp.Spec, store *mcp.OAuthStore) *MCPServers {
	return &MCPServers{path: path, specs: specs, store: store}
}

// Path is the file these servers were declared in. It is worth saying even when
// there are none: "there are none" is not actionable without saying where they
// would go.
func (s *MCPServers) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Store is where an authorization is written. It is open for the whole session
// whether or not a server needs it, because the one that needs a login is by
// definition the one that is not connected.
func (s *MCPServers) Store() *mcp.OAuthStore {
	if s == nil {
		return nil
	}
	return s.store
}

// Specs are the declarations this session read, connected or not. /mcp login
// reaches a server through them rather than through the connections, which is
// the difference between starting the authorization and needing it already done.
func (s *MCPServers) Specs() []mcp.Spec {
	if s == nil {
		return nil
	}
	return s.specs
}

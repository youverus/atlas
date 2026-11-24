package assembly

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"pi-go/internal/compact"
	"pi-go/internal/config"
	"pi-go/internal/mcp"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// MCPServers is the MCP half of a session: the connections, where they were
// configured, and where an authorization would be kept.
//
// The zero value is safe and is what every caller with nothing to connect to
// passes, which matters because the front ends carry this by value through four
// layers of call signatures. A live instance keeps one Set for its lifetime,
// even when initially empty, and retains every declaration for settings.
type MCPServers struct {
	set       *mcp.Set
	path      string
	store     *mcp.OAuthStore
	specs     []mcp.Spec
	lifecycle *mcpLifecycle
}

// Keep the lock behind a pointer so copies share the same lifecycle and Set.
type mcpLifecycle struct {
	mu     sync.Mutex
	home   string
	closed bool
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
	state := MCPServers{
		path:      cfg.Global,
		store:     store,
		lifecycle: &mcpLifecycle{home: home},
	}
	// The notes are what the configuration left out: a plugin that did not
	// load, a plugin server whose name was taken. They come first, because a
	// reader who sees an unexpected server list wants the reason before the
	// list of failures.
	notes := append([]string(nil), cfg.Notes...)
	for _, name := range cfg.Missing {
		notes = append(notes, fmt.Sprintf("mcp: ${%s} is not set, so a server in %s starts with the literal value", name, cfg.Global))
	}
	specs := make([]mcp.Spec, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		specs = append(specs, mcpSpec(server, store))
	}
	state.specs = make([]mcp.Spec, 0, len(specs)+len(cfg.Disabled))
	state.specs = append(state.specs, specs...)
	for _, server := range cfg.Disabled {
		state.specs = append(state.specs, mcpSpec(server, store))
	}
	sort.Slice(state.specs, func(i, j int) bool { return state.specs[i].Name < state.specs[j].Name })
	// The identity is this binary's: a server's log saying "atlas 0.7.0" is the
	// only way to tell which client an entry was tested with.
	state.set = mcp.Open(ctx, specs, mcp.Identity{Name: "atlas", Version: version})
	// A result too large for a message is written next to the session, same as
	// the shell's output: both are "here is the part you could not see", and two
	// destinations for one idea would be two things to clean up.
	state.set.Spill = spill
	return state, append(notes, state.set.Notes()...), nil
}

func mcpSpec(server config.MCPServer, store *mcp.OAuthStore) mcp.Spec {
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
	return spec
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

// SetEnabled saves the user's switch and applies it to the live set. An enabled
// server that fails to connect or needs login reports that status in Describe.
// Re-enabling an unavailable server retries the connection.
func (s *MCPServers) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if s == nil || s.lifecycle == nil {
		return fmt.Errorf("mcp: no live server set")
	}
	lifecycle := s.lifecycle
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.closed {
		return fmt.Errorf("mcp: server set is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	index := sort.Search(len(s.specs), func(i int) bool { return s.specs[i].Name >= name })
	if index == len(s.specs) || s.specs[index].Name != name {
		return fmt.Errorf("mcp: unknown server %q", name)
	}
	if err := config.SaveMCPEnabled(lifecycle.home, name, enabled); err != nil {
		return err
	}
	if enabled {
		s.set.Enable(ctx, s.specs[index])
	} else {
		s.set.Disable(name)
	}
	return nil
}

// Close ends every connection and prevents further changes to this live set.
func (s *MCPServers) Close() {
	if s == nil || s.lifecycle == nil {
		return
	}
	lifecycle := s.lifecycle
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.closed {
		return
	}
	lifecycle.closed = true
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

// Specs returns the enabled declarations, including servers that need login or
// failed to connect. /mcp login uses this list to find eligible servers.
func (s *MCPServers) Specs() []mcp.Spec {
	if s == nil {
		return nil
	}
	if s.set == nil {
		return slices.Clone(s.specs)
	}
	return s.set.Specs()
}

// MCPToolInfo describes one remote MCP tool allowed into the session.
type MCPToolInfo struct {
	Name        string
	Title       string
	WireName    string
	Description string
}

// MCPServerInfo is a plain snapshot of one configured MCP server's status and
// allowed tools, decoupled from internal/mcp types for adapters.
type MCPServerInfo struct {
	Name         string
	Version      string
	Transport    string
	State        string
	Declared     int
	Tools        []MCPToolInfo
	Missing      []string
	Error        string
	RefreshError string
}

// MCPSnapshot describes all configured MCP servers and where mcp.json lives.
type MCPSnapshot struct {
	ConfigPath string
	Servers    []MCPServerInfo
}

// MCPOverhead summarizes connected MCP servers, active remote tools, and their
// estimated declaration tokens.
type MCPOverhead struct {
	Connected  int
	Configured int
	Tools      int
	Tokens     int
}

// Overhead counts connected MCP servers, remote tools, and estimated token
// footprint of their declarations.
func (s *MCPServers) Overhead() MCPOverhead {
	if s == nil {
		return MCPOverhead{}
	}
	snapshot := s.Describe()
	connected := 0
	for _, server := range snapshot.Servers {
		if server.State == "connected" {
			connected++
		}
	}
	tools := s.Tools()
	decls := make([]wire.Tool, 0, len(tools))
	for _, item := range tools {
		decls = append(decls, wire.Tool{
			Name:        item.Name(),
			Description: item.Description(),
			Parameters:  item.Parameters(),
		})
	}
	return MCPOverhead{
		Connected:  connected,
		Configured: len(snapshot.Servers),
		Tools:      len(tools),
		Tokens:     compact.EstimateToolsTokens(decls),
	}
}

// Describe returns a snapshot of configured MCP servers and their tools.
func (s *MCPServers) Describe() MCPSnapshot {
	if s == nil {
		return MCPSnapshot{}
	}
	if s.set == nil {
		return MCPSnapshot{ConfigPath: s.path, Servers: []MCPServerInfo{}}
	}
	raw := s.Servers()
	active := make(map[string]*mcp.Server, len(raw))
	for _, server := range raw {
		active[server.Name] = server
	}
	out := make([]MCPServerInfo, 0, len(s.specs))
	for _, spec := range s.specs {
		transport := "stdio"
		if spec.URL != "" {
			transport = "http"
		}
		server := active[spec.Name]
		if server == nil {
			out = append(out, MCPServerInfo{Name: spec.Name, Transport: transport, State: "disabled"})
			continue
		}
		state := "connected"
		errText := ""
		switch {
		case server.NeedsLogin:
			state = "needs_login"
		case server.Err != nil:
			state = "failed"
			errText = server.Err.Error()
		}
		refreshErr := ""
		if server.RefreshErr != nil {
			refreshErr = server.RefreshErr.Error()
		}
		tools := make([]MCPToolInfo, 0, len(server.Tools))
		for _, remote := range server.Tools {
			tools = append(tools, MCPToolInfo{
				Name:        remote.Name,
				Title:       remote.Title,
				WireName:    remote.WireName,
				Description: remote.Description,
			})
		}
		out = append(out, MCPServerInfo{
			Name:         server.Name,
			Version:      server.Version,
			Transport:    transport,
			State:        state,
			Declared:     server.Declared,
			Tools:        tools,
			Missing:      append([]string(nil), server.Missing...),
			Error:        errText,
			RefreshError: refreshErr,
		})
	}
	return MCPSnapshot{
		ConfigPath: s.path,
		Servers:    out,
	}
}

// Refresh re-lists tools on the named connected MCP server, or on all
// connected servers when name is empty.
func (s *MCPServers) Refresh(name string) error {
	if s == nil || s.set == nil {
		return nil
	}
	return s.set.Refresh(name)
}

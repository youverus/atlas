package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"pi-go/internal/tool"
)

// Set is the MCP servers of one session.
//
// Servers connect independently and in parallel: one that fails becomes an entry
// holding the reason, because a broken server must not cost a session the other
// servers' tools. Failures are reported once as warnings and stay visible in
// /mcp afterwards, so "the server has no tools" and "the server's tools are
// missing" never look the same.
//
// The owner serializes Enable, Disable, and Close. Readers and refreshes can
// run concurrently with those operations.
type Set struct {
	identity Identity

	// mu guards the servers and the tool lists inside them. They are no longer
	// fixed: a server that says its tools changed is listed again while the
	// session runs, so every read has to be one that a refresh cannot tear.
	mu      sync.RWMutex
	servers []*Server

	// Spill is where a result too large for a message is written, so the notice
	// can name a path the model can read back. Nil means the OS temp directory:
	// a Set built by Open has no better answer until the caller supplies one,
	// because only the caller knows which session directory it belongs to.
	Spill *tool.Spill
}

// Server is one configured server in this session.
type Server struct {
	// Name is the configured name, which is what the user wrote.
	Name string
	// Version is what the server reported during the handshake. Empty for a
	// server that never got that far.
	Version string
	// Tools are what the server declared and the server's "tools" list let
	// through, in the server's own order and already renamed for the model.
	Tools []RemoteTool
	// Declared is how many tools the last listing held, before that list
	// narrowed them. It is equal to len(Tools) for a server without one, and it
	// is what makes "two of the five it declares" sayable.
	Declared int
	// Missing names the tools the "tools" list asks for that the server does not
	// declare. A whitelist that hides a tool is a decision; one that names a
	// tool nobody has is a typo, and this is the only place it surfaces.
	Missing []string
	// RefreshErr is why the tool list could not be read again after the server
	// said it changed. The list from before the notification stays in Tools: a
	// server that cannot answer tools/list right now is usually one that will.
	RefreshErr error
	// Err is why this server is missing from the session; nil when connected.
	Err error
	// NeedsLogin marks a server that authorizes with OAuth and has no token
	// stored. It is separate from Err because nothing failed: the server was
	// never asked anything, and the one action that changes the state is a
	// login.
	NeedsLogin bool

	spec    Spec
	session *client
	// live is the refresh bookkeeping, behind a pointer so that a snapshot of
	// this Server — which is what Servers returns — stays copyable.
	live *liveServer
}

// liveServer is the state a refresh needs and a reader does not.
type liveServer struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	// refreshing is true while a listing is in flight, and dirty records that
	// another notification arrived while it was: a server that changed twice in
	// one round trip is listed once more, not twice.
	refreshing bool
	dirty      bool
}

// Connected reports whether the server took part in this session.
func (s *Server) Connected() bool { return s.Err == nil && !s.NeedsLogin }

// Open connects to every spec and lists its tools.
//
// It returns a Set even when every server fails: the failures are the useful
// part, and a caller deciding what to warn about needs them as data rather than
// as one error that hides the others.
func Open(ctx context.Context, specs []Spec, id Identity) *Set {
	built := make([]*Server, len(specs))
	var wait sync.WaitGroup
	for index, spec := range specs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			built[index] = openServer(ctx, spec, id)
		}()
	}
	wait.Wait()

	// Sorting here rather than in the caller makes the tool list, the warnings
	// and /mcp agree on one order, and makes two runs of the same config produce
	// the same prompt.
	sort.SliceStable(built, func(i, j int) bool { return built[i].Name < built[j].Name })
	set := &Set{identity: id, servers: built}
	assignNames(built)
	// Install the hooks after publishing the servers so a pending notification
	// can refresh the live tool list.
	for _, server := range built {
		set.watch(server)
	}
	return set
}

// Enable connects and publishes one server. A connection failure is published
// as a server with no tools, so a later Enable can retry it.
func (s *Set) Enable(ctx context.Context, spec Spec) {
	s.mu.RLock()
	index, found := s.serverIndex(spec.Name)
	connected := found && s.servers[index].session != nil
	s.mu.RUnlock()
	if connected {
		return
	}

	server := openServer(ctx, spec, s.identity)
	s.mu.Lock()
	if found {
		s.servers[index] = server
	} else {
		s.servers = slices.Insert(s.servers, index, server)
	}
	assignNames(s.servers)
	s.mu.Unlock()
	s.watch(server)
}

// Disable removes one server from future snapshots, then closes its connection.
func (s *Set) Disable(name string) {
	s.mu.Lock()
	index, found := s.serverIndex(name)
	if !found {
		s.mu.Unlock()
		return
	}
	server := s.servers[index]
	s.servers = slices.Delete(s.servers, index, index+1)
	s.mu.Unlock()
	server.close()
}

// serverIndex searches the sorted servers with mu held.
func (s *Set) serverIndex(name string) (int, bool) {
	return slices.BinarySearchFunc(s.servers, name, func(server *Server, name string) int {
		return strings.Compare(server.Name, name)
	})
}

// openServer connects one server and lists its tools under one deadline.
func openServer(ctx context.Context, spec Spec, id Identity) *Server {
	server := &Server{Name: spec.Name, spec: spec}
	if err := ctx.Err(); err != nil {
		server.Err = err
		return server
	}
	// A server that authorizes with OAuth and has no token is not asked
	// anything. A 401 at startup would say "you are not logged in" more slowly
	// and nothing else, and the login is a command the user runs, not something
	// this path can complete.
	if spec.OAuth != nil && !spec.OAuth.authorized(spec.URL) {
		server.NeedsLogin = true
		return server
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	session, err := connect(ctx, spec, id)
	if err != nil {
		server.Err = err
		return server
	}
	server.Version = session.info.Version
	defs, err := session.listTools(ctx)
	if err != nil {
		// A server that handshakes but cannot list its tools is of no use: its
		// tools are the only thing this client wants from it.
		_ = session.Close()
		server.Err = err
		return server
	}
	server.session = session
	// A refresh belongs to the connection, which outlives the handshake.
	liveCtx, stop := context.WithCancel(context.Background())
	server.live = &liveServer{ctx: liveCtx, cancel: stop}
	server.setTools(defs)
	return server
}

func (s *Server) close() {
	if s.session == nil {
		return
	}
	s.live.cancel()
	s.session.watch(nil)
	_ = s.session.Close()
}

// setTools records one listing: everything the server declared, and the part the
// configured "tools" list lets through.
//
// Both numbers are kept because the pair is the answer to two different
// questions: how many tools this session has, and whether the list that narrowed
// them is doing what its author meant.
func (s *Server) setTools(defs []toolDef) {
	previousNames := make(map[string]string, len(s.Tools))
	for _, remote := range s.Tools {
		previousNames[remote.Name] = remote.WireName
	}
	s.Declared = len(defs)
	s.Tools = make([]RemoteTool, 0, len(defs))
	s.Missing = nil
	if len(s.spec.Tools) > 0 {
		s.Missing = make([]string, 0, len(s.spec.Tools))
	}
	declared := make(map[string]bool, len(defs))
	for _, def := range defs {
		declared[def.Name] = true
		if !s.spec.allows(def.Name) {
			continue
		}
		s.Tools = append(s.Tools, RemoteTool{
			Server:      s.Name,
			Name:        def.Name,
			Title:       def.Title,
			WireName:    previousNames[def.Name],
			Description: def.Description,
			Schema:      def.InputSchema,
		})
		delete(previousNames, def.Name)
	}
	for _, name := range s.spec.Tools {
		if !declared[name] {
			s.Missing = append(s.Missing, name)
		}
	}
}

// Servers returns a snapshot of the servers in sorted order, connected or not.
//
// It is a copy because the tool lists inside it move: a server that reported a
// change is listed again on a goroutine of its own, and a caller drawing a table
// must not read a slice that is being replaced underneath it.
func (s *Set) Servers() []*Server {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	servers := make([]*Server, len(s.servers))
	for index, server := range s.servers {
		snapshot := *server
		snapshot.Tools = append([]RemoteTool(nil), server.Tools...)
		snapshot.Missing = append([]string(nil), server.Missing...)
		servers[index] = &snapshot
	}
	return servers
}

// Specs returns a snapshot of the enabled server declarations in sorted order.
func (s *Set) Specs() []Spec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var specs []Spec
	for _, server := range s.servers {
		specs = append(specs, server.spec)
	}
	return specs
}

// Tools wraps every tool of every connected server for the model.
//
// It is read at the start of every turn (app.refreshTools), which is what makes
// a tool that appeared mid-conversation usable on the next question instead of
// after a restart.
func (s *Set) Tools() []tool.Tool {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var set []tool.Tool
	for _, server := range s.servers {
		if server.session == nil {
			continue
		}
		for _, remote := range server.Tools {
			set = append(set, &toolProxy{client: server.session, remote: remote, spill: s.Spill})
		}
	}
	return set
}

// Notes returns one line per failure, in server order. They are warnings rather
// than errors: the session goes on without that server.
func (s *Set) Notes() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var notes []string
	for _, server := range s.servers {
		switch {
		case server.NeedsLogin:
			// The one state whose next step is a command of its own, so the
			// note carries it. Saying "unavailable" would send the reader
			// looking for a network problem.
			notes = append(notes, fmt.Sprintf("mcp: server %q needs a login before its tools can be used; run \"/mcp login %s\"", server.Name, server.Name))
		case server.Err != nil:
			notes = append(notes, fmt.Sprintf("mcp: server %q is unavailable, its tools are missing from this session: %v", server.Name, server.Err))
		case len(server.Missing) > 0:
			// Not a failure, and still worth a line at startup: a whitelist
			// naming a tool nobody has hides the tools it was meant to keep.
			notes = append(notes, fmt.Sprintf("mcp: server %q does not declare %s, which the tools list in mcp.json asks for",
				server.Name, quoteList(server.Missing)))
		}
	}
	return notes
}

// Close shuts every connection down. It is idempotent because a failed startup
// may close the set it already built.
func (s *Set) Close() {
	if s == nil {
		return
	}
	s.mu.RLock()
	servers := slices.Clone(s.servers)
	s.mu.RUnlock()
	for _, server := range servers {
		server.close()
	}
}

// watch installs the tool-list hook and handles any earlier notification.
func (s *Set) watch(server *Server) {
	if server.session != nil && server.session.watch(func() { s.refresh(server) }) {
		s.refresh(server)
	}
}

// refresh lists one server again after it said its tools changed.
//
// One listing at a time per server, and at most one more for a burst: a server
// that adds fifty tools one notification at a time should cost two round trips,
// not fifty. Each listing gets the budget the startup one had, because it is the
// same request.
func (s *Set) refresh(server *Server) {
	server.live.mu.Lock()
	if server.live.refreshing {
		server.live.dirty = true
		server.live.mu.Unlock()
		return
	}
	server.live.refreshing = true
	for {
		server.live.mu.Unlock()
		err := s.relist(server)
		server.live.mu.Lock()
		if err != nil || !server.live.dirty {
			break
		}
		server.live.dirty = false
	}
	server.live.refreshing = false
	server.live.mu.Unlock()
}

// relist asks the server for its tools and installs the answer.
//
// A failure keeps the tools this session already had and records why: the server
// is still connected, so the list in hand is the best available guess about what
// it can still do.
func (s *Set) relist(server *Server) error {
	ctx, cancel := context.WithTimeout(server.live.ctx, handshakeTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	defs, err := server.session.listTools(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := server.live.ctx.Err(); err != nil {
		return err
	}
	if err != nil {
		server.RefreshErr = err
		return err
	}
	server.RefreshErr = nil
	server.setTools(defs)
	// Names are assigned across every server at once, so a tool that just
	// appeared cannot take a name another server already uses.
	assignNames(s.servers)
	return nil
}

// Refresh re-lists tools from the named connected server, or from every
// connected server when name is empty.
func (s *Set) Refresh(name string) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	servers := append([]*Server(nil), s.servers...)
	s.mu.RUnlock()

	if name != "" {
		for _, server := range servers {
			if server.Name != name {
				continue
			}
			if server.session == nil {
				return fmt.Errorf("mcp: server %q is not connected", name)
			}
			return s.relist(server)
		}
		return fmt.Errorf("mcp: unknown server %q", name)
	}

	var firstErr error
	for _, server := range servers {
		if server.session == nil {
			continue
		}
		if err := s.relist(server); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// quoteList formats names for one line of prose: "a", "b" and "c".
func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = strconv.Quote(name)
	}
	if len(quoted) < 2 {
		return strings.Join(quoted, "")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// ToolPrefix starts every name this package hands the model. It is exported
// because the footer groups tools by it, and a second copy of the prefix would be
// the bug that leaves remote tools in a column of their own.
const ToolPrefix = "mcp__"

// toolNameLimit is the ceiling for one wire name. OpenAI's function names are
// the tightest published limit of the three providers, so it is the one that has
// to hold; a longer name is truncated and hashed rather than refused.
const toolNameLimit = 64

// assignNames gives every remote tool the name the model sees.
//
// The spelling is pi's and Codex's: mcp__<server>__<tool>. Matching them is not
// taste — the same entry in two clients should produce the same tool, and a
// model that has met one should recognise the other.
//
// Reserve published names before assigning new ones so toggling a server does
// not rename tools on a different connection with a colliding normalized name.
func assignNames(servers []*Server) {
	seen := make(map[string]bool, 16)
	for _, server := range servers {
		for _, remote := range server.Tools {
			if remote.WireName != "" {
				seen[remote.WireName] = true
			}
		}
	}
	for _, server := range servers {
		for index := range server.Tools {
			remote := &server.Tools[index]
			if remote.WireName != "" {
				continue
			}
			name := normalizeName(ToolPrefix + server.Name + "__" + remote.Name)
			if seen[name] || len(name) > toolNameLimit {
				name = hashedName(name, remote.Server, remote.Name)
			}
			// A hash collision over two distinct pairs is not something a user
			// will meet, but two tools under one name is worse than a longer
			// name, so uniqueness wins over length here.
			for seen[name] {
				name += "_"
			}
			seen[name] = true
			remote.WireName = name
		}
	}
}

// normalizeName replaces everything a provider may refuse. One rune becomes one
// underscore on purpose: "read file" and "read-file" must stay distinguishable
// instead of collapsing into one name.
func normalizeName(name string) string {
	var builder strings.Builder
	builder.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

// hashedName truncates a name and appends eight hex digits of the pair's hash.
func hashedName(name, server, tool string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	suffix := hex.EncodeToString(sum[:4])
	keep := toolNameLimit - len(suffix) - 1
	if len(name) > keep {
		// normalizeName leaves ASCII only, so cutting at a byte is safe.
		name = name[:keep]
	}
	return name + "_" + suffix
}

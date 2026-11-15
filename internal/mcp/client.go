package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"pi-go/internal/tool"
)

// client is one initialized connection to one server.
//
// Everything above the transport lives here: the handshake, listing, calling,
// the progress a long call reports, and the one repair an HTTP session needs. The
// transport below sees messages, not methods, which is what keeps the two
// transports interchangeable.
type client struct {
	spec Spec
	tr   transport
	id   Identity

	// info and version are what the handshake settled. The version is the
	// server's, which may be older than the one this client asked for.
	info    Info
	version string

	// mu guards the calls in flight, keyed by the progress token they carried.
	// A notification names nothing but that token, so the lookup is a map
	// read; tokens are handed out from the same mutex. It also guards the
	// handoff of tool-list changes, which arrives on the same path.
	mu        sync.Mutex
	calls     map[string]*callState
	nextToken int64

	// onToolsChanged is where a list_changed goes once something is listening.
	// It is installed by watch after the handshake, and until then
	// toolsChanged remembers that one arrived, so a notification that lands in
	// the window between the first listing and the hook is not lost.
	onToolsChanged func()
	toolsChanged   bool
}

// methodToolsChanged is the notification that says the server's tool list is
// not what it was. It is the second message this client acts on and the only
// one whose effect outlives the call that triggered it.
const methodToolsChanged = "notifications/tools/list_changed"

// initializeParams is the client's half of the handshake. Capabilities is empty
// on purpose: this client offers no roots, no sampling, no elicitation, and
// declaring a capability it does not serve would invite a server to use it.
type initializeParams struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Capabilities    struct{} `json:"capabilities"`
	ClientInfo      Identity `json:"clientInfo"`
}

// initializeResult is the server's half. Nothing here is optional enough to
// skip: the version decides compatibility and serverInfo is what /mcp shows.
// The instructions field is not decoded, because the first version has nowhere
// to put it.
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      Info   `json:"serverInfo"`
}

// connect starts the transport and completes the handshake. A failed handshake
// closes the transport: a connection to a server that answered the wrong version
// has no use that would justify keeping it.
func connect(ctx context.Context, spec Spec, id Identity) (*client, error) {
	tr, err := spec.newTransport()
	if err != nil {
		return nil, err
	}
	c := &client{spec: spec, tr: tr, id: id, calls: make(map[string]*callState)}
	// The receiver goes in before the handshake. The GET channel starts right
	// after it, and a transport that is already reading must not have to hold a
	// notification until somebody is listening.
	tr.listen(c.notify)
	if err := c.handshake(ctx); err != nil {
		_ = tr.Close()
		return nil, err
	}
	return c, nil
}

// handshake runs initialize, checks the version, and announces the client is
// ready.
//
// The version check is this client's only compatibility rule: a server that
// answers with a revision outside the list is refused by name and version,
// because the alternative is running a state machine on guesses. Everything else
// is left as the server sent it.
func (c *client) handshake(ctx context.Context) error {
	name := c.id.Name
	if name == "" {
		name = clientName
	}
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		ClientInfo:      Identity{Name: name, Version: c.id.Version},
	}
	raw, err := c.tr.Call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	var answer initializeResult
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if !supports(answer.ProtocolVersion) {
		return versionError(c.spec.Name, answer.ProtocolVersion)
	}
	c.version = answer.ProtocolVersion
	c.info = answer.ServerInfo
	// HTTP repeats the version on every request, so the transport has to learn
	// which one the server picked. stdio settled it with the handshake itself.
	if tr, ok := c.tr.(negotiable); ok {
		tr.negotiate(answer.ProtocolVersion)
	}
	if err := c.tr.Notify(ctx, "notifications/initialized", nil); err != nil {
		return err
	}
	// Only now is there a session to listen on. A transport that needs a channel
	// of its own opens it here and nowhere else: a second handshake after a
	// forgotten session must not open a second one.
	if opener, ok := c.tr.(streamer); ok {
		opener.openStream()
	}
	return nil
}

// call is every request this client makes after the handshake. It exists for one
// repair: an HTTP server that answers 404 to a request carrying a session id has
// forgotten the session, and the spec says to initialize again.
//
// Retrying is safe only here, and the reason is narrow: a session the server has
// no record of is a request it never dispatched. Nothing else is retried, which
// is why tools/call cannot accidentally run twice.
func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := c.tr.Call(ctx, method, params)
	if !errors.Is(err, errSessionExpired) {
		return raw, err
	}
	if err := c.handshake(ctx); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return c.tr.Call(ctx, method, params)
}

// toolDef is one tool as a server describes it. The schema is kept as raw JSON
// because it is a JSON Schema: passing it to the model unchanged is the whole
// point, and re-encoding it through a Go type would lose the parts not named
// here.
type toolDef struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// listTools reads every page of the tool list.
//
// The cursor loop is followed rather than assumed short: a server is allowed to
// page, and a client that reads only the first page silently loses tools, which
// looks exactly like a server that never had them.
func (c *client) listTools(ctx context.Context) ([]toolDef, error) {
	var all []toolDef
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var answer struct {
			Tools      []toolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		all = append(all, answer.Tools...)
		if answer.NextCursor == "" {
			return all, nil
		}
		cursor = answer.NextCursor
	}
	return nil, fmt.Errorf("tools/list: more than %d pages, and the cursor is still not empty", maxListPages)
}

// callParams is one tools/call. Arguments is omitted rather than sent as null
// when the model passed nothing, because a server is not required to accept
// null where it expects an object.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Meta carries the progress token. It goes on every call, not only the ones a
	// person is watching, because the token is also what this client needs to
	// tell a slow call from a stuck one. A server with nothing to report ignores
	// it, and the spec defines no way to ask whether it will.
	Meta *callMeta `json:"_meta,omitempty"`
}

// callMeta is the only request metadata this client sends.
type callMeta struct {
	ProgressToken string `json:"progressToken"`
}

// callResult is the tools/call answer.
//
// IsError is the server reporting that the tool failed. It is not a transport
// error: the call worked, the tool did not, and the model needs to see the
// difference.
type callResult struct {
	Content           []contentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// contentBlock is one block of a result. The spec's five types are all fields of
// one struct for the same reason a message is: the discriminator is the type
// string, and a union of Go types would only add a decode step.
type contentBlock struct {
	Type     string    `json:"type"`
	Text     string    `json:"text"`
	Data     string    `json:"data"`
	MIMEType string    `json:"mimeType"`
	Resource *resource `json:"resource"`
	URI      string    `json:"uri"`
	Name     string    `json:"name"`
}

// resource is the embedded content of a resource block.
type resource struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	Text     string `json:"text"`
	Blob     string `json:"blob"`
}

// callTool runs one tools/call.
//
// The budget comes from the server's configuration, which is why it is applied
// here and not in the wrapper above: the wrapper is the part that has to stay
// ignorant of it. It bounds silence rather than the whole call — every progress
// notification restarts it (progress.go), so a tool that reports while it works
// is not killed for being slow.
//
// There is no retry beyond the session repair in call: a tool that wrote a file
// before the connection dropped has already done it.
func (c *client) callTool(ctx context.Context, name string, arguments json.RawMessage, onUpdate func(tool.Partial)) (callResult, error) {
	// A timer, not a context deadline: a deadline cannot be pushed back, and
	// pushing it back is the feature. Cancelling the child context is how a call
	// that went quiet is taken out of the transport.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := c.track(cancel, c.spec.timeout(), onUpdate)
	defer c.untrack(state)

	params := callParams{Name: name, Meta: &callMeta{ProgressToken: state.token}}
	if len(arguments) > 0 {
		params.Arguments = arguments
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		if state.abandoned() {
			return callResult{}, fmt.Errorf("%s: silent for %s, so the call was abandoned; a server that reports progress restarts this clock, so raise this server's \"timeout\" only if it cannot", name, shortDuration(state.idle))
		}
		return callResult{}, err
	}
	var answer callResult
	if err := json.Unmarshal(raw, &answer); err != nil {
		return callResult{}, fmt.Errorf("tools/call %s: %w", name, err)
	}
	return answer, nil
}

// track registers one call and starts its clock. The token is the string form of
// a counter: unique inside this connection, and compared the same way whether the
// server echoes it as a string or as the number it stands for.
func (c *client) track(cancel context.CancelFunc, idle time.Duration, update func(tool.Partial)) *callState {
	c.mu.Lock()
	c.nextToken++
	state := &callState{
		token:  strconv.FormatInt(c.nextToken, 10),
		idle:   idle,
		cancel: cancel,
		update: update,
	}
	c.calls[state.token] = state
	c.mu.Unlock()

	state.mu.Lock()
	state.arm()
	state.mu.Unlock()
	return state
}

// untrack forgets one call. The clock stops before the entry goes away, so a
// notification landing mid-cleanup finds a closed state instead of restarting a
// clock nobody is waiting on.
func (c *client) untrack(state *callState) {
	state.finish()
	c.mu.Lock()
	delete(c.calls, state.token)
	c.mu.Unlock()
}

// notify routes one server-initiated message. Two methods have a consumer.
func (c *client) notify(message incoming) {
	switch message.Method {
	case methodProgress:
		c.noteProgress(message)
	case methodToolsChanged:
		c.noteToolsChanged()
	}
}

// noteProgress hands one progress notification to the call that asked for it.
func (c *client) noteProgress(message incoming) {
	var note progressNote
	if err := json.Unmarshal(message.Params, &note); err != nil {
		return
	}
	c.mu.Lock()
	state := c.calls[fmt.Sprint(note.ProgressToken)]
	c.mu.Unlock()
	if state == nil {
		// Progress for a call that is already over, or for a token this client
		// never issued. There is nobody left to tell.
		return
	}
	state.note(note)
}

// noteToolsChanged acts on notifications/tools/list_changed.
//
// The re-list runs on a goroutine of its own because this is called by the
// goroutine that reads the connection: a request written from here would wait
// for an answer that only that goroutine can deliver.
func (c *client) noteToolsChanged() {
	c.mu.Lock()
	hook := c.onToolsChanged
	if hook == nil {
		c.toolsChanged = true
	}
	c.mu.Unlock()
	if hook != nil {
		go hook()
	}
}

// watch installs the receiver of tool-list changes, and reports whether one
// arrived before there was anywhere to send it. The caller re-lists when it did:
// the notification is a fact about the server, not about the listener.
func (c *client) watch(hook func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onToolsChanged = hook
	missed := c.toolsChanged
	c.toolsChanged = false
	return missed
}

// Close ends the connection.
func (c *client) Close() error { return c.tr.Close() }

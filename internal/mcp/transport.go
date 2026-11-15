package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// transport carries the messages of one connection. There are two
// implementations and the client above them sees only method calls.
//
// Correlation lives inside the transport because the two transports correlate
// differently: stdio demultiplexes one shared pipe by id, HTTP reads one
// response body per POST. Pushing either mechanism up would make the client
// speak a transport.
type transport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Notify(ctx context.Context, method string, params any) error
	// listen registers the receiver for messages the server sends on its own.
	// It is called once, before the handshake: a transport that is already
	// reading cannot swap receivers without deciding what happens to whatever
	// arrives in between.
	listen(sink notificationSink)
	Close() error
}

// notificationSink receives the messages a server sends unprompted: progress
// for a call in flight, a tool list that changed, log records.
//
// It runs on the goroutine that reads the connection, so an implementation has
// to be quick and must not call back into the transport. A nil sink is legal and
// means "drop them", which is what a test that only cares about request and
// response gets.
type notificationSink func(incoming)

// streamer is implemented by transports that need to open a channel of their
// own before server-initiated messages can arrive. stdio already has one — the
// pipe carries both directions — while streamable HTTP has to ask for a GET.
type streamer interface {
	openStream()
}

// negotiable is implemented by transports that repeat the protocol version on
// every message. stdio does not need it — the handshake settles the version for
// the life of the pipe — while streamable HTTP sends it as a header on every
// request, so the version the server picked has to reach it after `initialize`.
type negotiable interface {
	negotiate(version string)
}

// Errors a caller can tell apart. The transport-level ones are wrapped so that
// errors.Is works through the context the client adds.
var (
	// errIncompleteMessage is a message that ended without its newline. It is a
	// failure rather than a truncation: JSON-RPC over stdio is line framed, and
	// a partial line means the writer died mid-message.
	errIncompleteMessage = errors.New("incomplete JSON-RPC message")

	// errMessageTooLong is one line that exceeded the frame budget. The
	// connection is unusable afterwards: the rest of the line is still in the
	// pipe and would be read as a message of its own.
	errMessageTooLong = errors.New("message exceeds the frame size limit")

	// errUnauthorized is a 401 or a 403 from a server this session has no token
	// for. It names both ways to get one, because which of them applies is the
	// server's decision and not something this client can discover.
	errUnauthorized = errors.New(`the server requires authentication: put a token in the "headers" of this server, or set "auth": "oauth" and run "/mcp login <server>"`)

	// errSessionExpired is a 404 on a request that carried a session id: the
	// server forgot the session and the client has to handshake again.
	errSessionExpired = errors.New("the session expired")
)

// tailBuffer keeps the last limit bytes written to it. A stdio server writes the
// reason it failed to stderr and nowhere else; forwarding that to the terminal
// would interleave it with the interface, so it is kept and quoted in the error
// instead.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	text  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text = append(b.text, p...)
	if len(b.text) > b.limit {
		b.text = b.text[len(b.text)-b.limit:]
	}
	return len(p), nil
}

// String returns the tail with the newest content intact.
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.text))
}

// waitClosed reports whether done closed within d.
func waitClosed(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// quoteForError folds a server's stderr into one line, because the place it is
// printed is a warning or an error message and neither has room for a stack
// trace. The last line wins: it is the one a wrapper usually writes last.
func quoteForError(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			if len(line) > 300 {
				line = line[:300] + "…"
			}
			return line
		}
	}
	return ""
}

// describeParams encodes one params value, leaving it out when there is none.
// A JSON-RPC message with "params": null is legal and noisy; the spec's
// examples omit the member instead.
func describeParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode params: %w", err)
	}
	return encoded, nil
}

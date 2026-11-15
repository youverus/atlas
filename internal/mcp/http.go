package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"pi-go/internal/sse"
)

// Headers of the streamable HTTP transport. They are not part of JSON-RPC: a
// gateway uses the session id to find its state and the version to pick a
// dialect, so both have to be on every request after the handshake.
const (
	sessionHeader = "Mcp-Session-Id"
	versionHeader = "MCP-Protocol-Version"

	// maxResponseBytes bounds one response body. It matches the stdio frame
	// limit: a result larger than this is a server that should have paged, and
	// reading it whole would turn one bad server into a memory problem.
	maxResponseBytes = 8 << 20

	// closeTimeout bounds the DELETE that ends a session. A shutdown must not
	// wait on a server that has stopped answering.
	closeTimeout = 5 * time.Second
)

// httpTransport speaks streamable HTTP: one POST per message, answered either by
// a single JSON body or by an event stream, plus one GET whose only job is the
// messages the server sends on its own.
//
// Both of the server's channels are read. Progress reaches the client either
// inside the POST's own event stream or on the GET, and servers differ on which
// they use: one that reports while a tool runs usually answers on the POST, while
// one that reports between requests has nowhere but the GET. Read handlers are
// written to the wrong one, so both are wired.
//
// The GET is best effort. A server that predates it answers 405 and the session
// continues without server-initiated messages, which is a smaller loss than the
// warning-per-server a stricter rule would produce.
type httpTransport struct {
	spec   Spec
	client *http.Client

	// oauth is non-nil for a server that authorizes with a token this client
	// obtained itself. tokens is the current one and is replaced by a refresh;
	// refreshing is the refresh a burst of 401s shares. All three belong to mu,
	// which is what makes two parallel 401s one refresh instead of two.
	oauth      *OAuthSpec
	tokens     OAuthTokens
	refreshing *refreshCall

	mu      sync.Mutex
	session string
	version string
	nextID  int64
	sink    notificationSink

	// streamCancel ends the GET channel. It is set once by openStream, and
	// streamOnce is what keeps one connection to one GET: a second handshake
	// after a forgotten session does not open a second stream, because the
	// stream belongs to the session that exists.
	streamCancel context.CancelFunc
	streamOnce   sync.Once

	closeOnce sync.Once
}

// newHTTP builds the transport. The client has no Timeout field on purpose:
// every request carries a context with a deadline, and a second, invisible
// deadline would make the error name the wrong one.
func newHTTP(spec Spec) (transport, error) {
	parsed, err := url.Parse(spec.URL)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", spec.Name, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("server %q: the url must start with http:// or https://", spec.Name)
	}
	t := &httpTransport{spec: spec, client: &http.Client{}}
	if spec.OAuth != nil {
		// The token the last login stored. An empty one is not an error here:
		// Set.Open decides not to connect at all in that case, and this
		// transport is also reachable from tests.
		t.oauth = spec.OAuth
		if entry := spec.OAuth.Store.Entry(spec.URL); entry.Tokens != nil {
			t.tokens = *entry.Tokens
		}
	}
	return t, nil
}

// negotiate records the version the server picked, which every later request
// repeats as a header.
func (t *httpTransport) negotiate(version string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = version
}

// listen stores the receiver for server-initiated messages. Both readers — the
// POST answer stream and the GET — hand their notifications to it.
func (t *httpTransport) listen(sink notificationSink) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sink = sink
}

// deliverNotification calls the sink outside the lock. The sink keeps its own
// state, and holding this one would put a slow interface update in front of
// every reply this connection is waiting for.
func (t *httpTransport) deliverNotification(message incoming) {
	t.mu.Lock()
	sink := t.sink
	t.mu.Unlock()
	if sink != nil {
		sink(message)
	}
}

// Call sends one request and waits for its answer.
func (t *httpTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	payload, err := describeParams(params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	id := t.newID()
	messages, err := t.exchange(ctx, rpcRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: payload}, id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	for _, message := range messages {
		// A server request inside the answer stream is answered the same way
		// stdio answers one: with -32601. Saying so beats leaving it to time out.
		if message.isRequest() {
			t.reject(ctx, message)
			continue
		}
		if message.isResponse() && message.ID != nil && *message.ID == id {
			if message.Error != nil {
				return nil, fmt.Errorf("%s: %w", method, message.Error)
			}
			return message.Result, nil
		}
	}
	return nil, fmt.Errorf("%s: %w", method, errors.New("the server answered without a response"))
}

// Notify sends a message that expects no answer.
func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	payload, err := describeParams(params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if _, err := t.exchange(ctx, rpcRequest{JSONRPC: jsonrpcVersion, Method: method, Params: payload}, 0); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	return nil
}

// exchange POSTs one message and returns what came back.
//
// wantID is the response the caller is waiting for, or zero for a message that
// expects none. It matters because an event stream stays open: without it, a
// server that keeps the connection alive would keep the caller waiting for an
// answer it already has.
//
// Two attempts at most, and the second only after a 401 that a refresh repaired.
// A third would be a server that keeps refusing, which is a message rather than a
// loop. Retrying is safe here because a refused request was never dispatched.
func (t *httpTransport) exchange(ctx context.Context, message any, wantID int64) ([]incoming, error) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	for attempt := 0; ; attempt++ {
		response, sent, err := t.post(ctx, encoded)
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusUnauthorized && t.oauth != nil && attempt == 0 {
			_ = response.Body.Close()
			if err := t.reauthorize(ctx, sent.token); err != nil {
				return nil, err
			}
			continue
		}
		return t.read(response, sent, wantID)
	}
}

// attempt is what one POST carried, so that its answer can be read against it.
type attempt struct {
	token   string
	session string
}

// post sends one encoded message.
func (t *httpTransport) post(ctx context.Context, encoded []byte) (*http.Response, attempt, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.spec.URL, bytes.NewReader(encoded))
	if err != nil {
		return nil, attempt{}, err
	}
	sent := t.prepare(request)
	response, err := t.client.Do(request)
	if err != nil {
		return nil, attempt{}, err
	}
	t.keepSession(response)
	return response, sent, nil
}

// prepare sets the headers of a POST and reports what it sent.
func (t *httpTransport) prepare(request *http.Request) attempt {
	request.Header.Set("Content-Type", "application/json")
	// Both are offered because the server chooses the shape. A single JSON body
	// is cheaper; a stream is the only way it can send more than one message.
	request.Header.Set("Accept", "application/json, text/event-stream")
	return t.sign(request)
}

// sign sets what every request of this session carries, whatever its method: the
// configured headers, the token, the session id and the negotiated version. The
// GET stream needs the last three as much as a POST does, which is why they live
// below the method-specific headers.
//
// The OAuth token is a fallback rather than the rule: a server configured with an
// Authorization header has its credential there, and offering two sources for one
// header would mean choosing one of them on the user's behalf.
func (t *httpTransport) sign(request *http.Request) attempt {
	for name, value := range t.spec.Headers {
		request.Header.Set(name, value)
	}

	t.mu.Lock()
	sent := attempt{session: t.session}
	version := t.version
	if t.oauth != nil {
		sent.token = t.tokens.Access
	}
	t.mu.Unlock()

	if sent.token != "" && request.Header.Get("Authorization") == "" {
		request.Header.Set("Authorization", "Bearer "+sent.token)
	}
	if sent.session != "" {
		request.Header.Set(sessionHeader, sent.session)
	}
	if version != "" {
		request.Header.Set(versionHeader, version)
	}
	return sent
}

// read turns one answer into messages, or into one of the named failures.
func (t *httpTransport) read(response *http.Response, sent attempt, wantID int64) ([]incoming, error) {
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		if t.oauth != nil {
			// The token was refused even though this connection has one. A 401
			// here has already been through one refresh (exchange), so the next
			// step is a login; a 403 never refreshes, because the server is
			// answering about permissions rather than about the token, and the
			// repair the spec defines for that — asking for more scope — is not
			// implemented in this version.
			return nil, errReauthorize
		}
		return nil, errUnauthorized
	case response.StatusCode == http.StatusNotFound && sent.session != "":
		// The server has no memory of this session. Forgetting it here lets the
		// next handshake store the new one; the client above retries once.
		t.mu.Lock()
		t.session = ""
		t.mu.Unlock()
		return nil, errSessionExpired
	case response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent:
		return nil, nil
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("the server answered %s: %s", response.Status, quoteForError(readErrorBody(response.Body)))
	}
	return decodeMessages(response, wantID, t.deliverNotification)
}

// refreshCall is one refresh in flight. Parallel requests that meet the same
// refusal wait on it instead of starting their own: a refresh token that has been
// rotated cannot be spent twice, and the second attempt would fail and take the
// new authorization down with it.
type refreshCall struct {
	done chan struct{}
	err  error
}

// reauthorize replaces the token the server just refused.
//
// rejected is the token the failed request carried. When the current one is
// already different, somebody else has refreshed it and this request only has to
// be retried: refreshing here would spend a refresh token on a token that works.
func (t *httpTransport) reauthorize(ctx context.Context, rejected string) error {
	t.mu.Lock()
	if t.tokens.Access != rejected {
		t.mu.Unlock()
		return nil
	}
	if call := t.refreshing; call != nil {
		t.mu.Unlock()
		select {
		case <-call.done:
			// The error was written before the channel was closed, and every
			// waiter gets the same one: one refusal is one cause.
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	t.refreshing = call
	previous := t.tokens
	t.mu.Unlock()

	// The network call runs with no lock held: a refresh that hangs must not stop
	// the other requests this session is serving.
	refreshed, err := refreshAccess(ctx, t.oauth, t.spec.URL, previous)

	t.mu.Lock()
	call.err = err
	if err == nil {
		t.tokens = refreshed
	}
	t.refreshing = nil
	t.mu.Unlock()
	close(call.done)
	return err
}

// keepSession stores the session id an initialize answer carries. A server that
// does not use sessions never sends the header, which is legal.
func (t *httpTransport) keepSession(response *http.Response) {
	session := response.Header.Get(sessionHeader)
	if session == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.session = session
}

// decodeMessages reads one answer body. The two shapes are told apart by the
// content type the server chose, not by sniffing the bytes.
func decodeMessages(response *http.Response, wantID int64, sink notificationSink) ([]incoming, error) {
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return decodeStream(response.Body, wantID, sink)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read the answer: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var message incoming
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("read the answer: %w: %q", err, clamp(string(body), 120))
	}
	return []incoming{message}, nil
}

// decodeStream reads events until the answer arrives or the stream ends. Every
// event's data is one JSON-RPC message, which is why the SSE reader is the same
// one the model adapters use.
//
// Notifications are handed to the sink as they are read rather than collected:
// progress on this stream is only worth anything while the call it belongs to is
// still running, and the response that ends the read is exactly when it stops
// mattering.
func decodeStream(body io.Reader, wantID int64, sink notificationSink) ([]incoming, error) {
	reader := sse.NewReader(io.LimitReader(body, maxResponseBytes))
	var messages []incoming
	for {
		event, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return messages, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read the answer stream: %w", err)
		}
		if strings.TrimSpace(event.Data) == "" {
			continue
		}
		var message incoming
		if err := json.Unmarshal([]byte(event.Data), &message); err != nil {
			return nil, fmt.Errorf("read the answer stream: %w: %q", err, clamp(event.Data, 120))
		}
		if message.isNotification() {
			if sink != nil {
				sink(message)
			}
			continue
		}
		messages = append(messages, message)
		if wantID != 0 && message.isResponse() && message.ID != nil && *message.ID == wantID {
			return messages, nil
		}
	}
}

// reject answers a server-initiated request with -32601. The reply is courtesy:
// a failure to deliver it changes nothing this client does, so it is dropped
// rather than turned into the answer's error.
func (t *httpTransport) reject(ctx context.Context, message incoming) {
	answer := rpcResponse{JSONRPC: jsonrpcVersion, ID: message.ID, Error: &rpcError{
		Code:    codeMethodNotFound,
		Message: "method not supported by this client: " + message.Method,
	}}
	if _, err := t.exchange(ctx, answer, 0); err != nil {
		return
	}
}

// readErrorBody folds a few hundred bytes of an error body into one line: what a
// failed request prints is a warning, and a warning is one line.
func readErrorBody(body io.Reader) string {
	text, err := io.ReadAll(io.LimitReader(body, 4<<10))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(text))
}

// newID hands out the ids of this connection. They only have to be unique inside
// one session; the JSON-RPC spec does not require them to start anywhere.
func (t *httpTransport) newID() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	return t.nextID
}

// openStream starts the GET channel, once per connection.
//
// No reconnect: the stream belongs to one session. A session the server has
// forgotten is rebuilt by the next request, and a stream that ended would be
// talking about the session that is gone, so a second attempt would either be
// refused or answer for state nobody has. The cost is written down in
// docs/mcp.md section 4: after such a repair, progress arrives only on the POST
// answer stream.
func (t *httpTransport) openStream() {
	t.streamOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		t.mu.Lock()
		t.streamCancel = cancel
		t.mu.Unlock()
		go t.readStream(ctx)
	})
}

// readStream reads server-initiated messages until the connection or the session
// ends.
//
// Every failure here is silent, and that is the design: this channel carries
// only what the client can live without, and a server that does not implement
// GET (405) is not broken. Reporting it would spend a warning per server per
// session on a message with no action behind it.
func (t *httpTransport) readStream(ctx context.Context) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.spec.URL, nil)
	if err != nil {
		return
	}
	t.sign(request)
	request.Header.Set("Accept", "text/event-stream")
	response, err := t.client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 405 for a server that does not offer the channel, 401 for one whose
		// token expired while nothing was happening — the POST path repairs the
		// second case the next time it matters.
		return
	}

	reader := sse.NewReader(response.Body)
	for {
		event, err := reader.Next()
		if err != nil {
			return
		}
		if strings.TrimSpace(event.Data) == "" {
			continue
		}
		var message incoming
		if err := json.Unmarshal([]byte(event.Data), &message); err != nil {
			// One unreadable event is a server bug, not a reason to stop reading
			// a channel that may still carry the next progress notification.
			continue
		}
		switch {
		case message.isNotification():
			t.deliverNotification(message)
		case message.isRequest():
			t.reject(ctx, message)
		}
	}
}

// Close ends the session. DELETE is what tells an HTTP server to free it; a
// server that does not implement DELETE answers 405 and is left to its own
// expiry, which is why the result is ignored either way.
//
// The GET channel goes first: it is a request the server holds open, and a
// DELETE sent while it is still being read would race with the body it is
// draining.
func (t *httpTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		cancel := t.streamCancel
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}

		t.mu.Lock()
		session, token := t.session, t.tokens.Access
		t.mu.Unlock()
		if session == "" {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.spec.URL, nil)
		if err != nil {
			return
		}
		for name, value := range t.spec.Headers {
			request.Header.Set(name, value)
		}
		if t.oauth != nil && token != "" && request.Header.Get("Authorization") == "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set(sessionHeader, session)
		response, err := t.client.Do(request)
		if err != nil {
			return
		}
		_ = response.Body.Close()
	})
	return nil
}

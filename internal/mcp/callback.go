package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// callbackPath is the one path the loopback listener answers.
	callbackPath = "/callback"

	// callbackPage is what the browser ends on. It is plain text on purpose: a
	// terminal browser renders HTML as a wall of markup, and the one thing this
	// page has to say fits in a line.
	callbackPage = "atlas is logged in to this server. You can close this window.\n"

	// callbackHeaderTimeout bounds reading one request's headers. The listener is
	// on the loopback interface, but a port is a port: this is what stops a
	// connection that never sends anything from holding the login open.
	callbackHeaderTimeout = 10 * time.Second
)

// callbackResult is what the browser brought back. Exactly one of the two is set.
type callbackResult struct {
	Code string
	Err  error
}

// callbackServer is the loopback listener the authorization server redirects to.
// It is a browser's endpoint, not a UI: it says one line and closes.
type callbackServer struct {
	listener net.Listener
	server   *http.Server

	// state is the value this login generated; issuer is the authorization
	// server's, already without a trailing slash.
	state  string
	issuer string

	result  chan callbackResult
	deliver sync.Once
	closing sync.Once
}

// startCallback listens on a random loopback port.
//
// The address is the 127.0.0.1 literal rather than "localhost": localhost may
// resolve to ::1 first, and a redirect URI that says 127.0.0.1 is the one address
// every browser agrees on. The port is random because a fixed one is a port that
// is sometimes taken, and the redirect URI is registered with the authorization
// server before the user has a chance to retry.
func startCallback(state, issuer string) (*callbackServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("the login needs a loopback port to receive the callback: %w", err)
	}
	callback := &callbackServer{
		listener: listener,
		state:    state,
		issuer:   strings.TrimSuffix(issuer, "/"),
		result:   make(chan callbackResult, 1),
	}
	callback.server = &http.Server{
		Handler:           callback,
		ReadHeaderTimeout: callbackHeaderTimeout,
	}
	go func() { _ = callback.server.Serve(listener) }()
	return callback, nil
}

// redirectURI is the address registered with the authorization server.
func (c *callbackServer) redirectURI() string {
	return "http://" + c.listener.Addr().String() + callbackPath
}

// ServeHTTP answers the browser.
//
// The two checks come before the code is accepted, and both refusals are silent to
// the terminal: a callback that fails a check was not this login's result, and
// reporting it as one would let a stranger's request end a wait that is still
// valid.
func (c *callbackServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != callbackPath {
		http.NotFound(w, r)
		return
	}
	query := r.URL.Query()

	// state first: the rest of the query is only this login's answer because of
	// it. A mismatch is not delivered — the real callback is still expected.
	if query.Get("state") != c.state {
		writeCallbackPage(w, http.StatusBadRequest,
			"atlas refused this callback: its state does not match the login that is waiting.\nNothing was authorized. Start /mcp login again from the terminal.\n")
		return
	}
	// RFC 9207: when the metadata named an issuer, an answer that names a
	// different one is a token from another authorization server, replayed here.
	if iss := strings.TrimSuffix(query.Get("iss"), "/"); iss != "" && c.issuer != "" && iss != c.issuer {
		c.finish(callbackResult{Err: fmt.Errorf("the callback says it came from %q, but this login started at %q; refusing it", iss, c.issuer)})
		writeCallbackPage(w, http.StatusBadRequest,
			"atlas refused this callback: it came from a different authorization server.\nNothing was authorized. Start /mcp login again from the terminal.\n")
		return
	}
	if refused := query.Get("error"); refused != "" {
		c.finish(callbackResult{Err: refusalError(refused, query.Get("error_description"))})
		writeCallbackPage(w, http.StatusOK,
			"atlas did not get an authorization. You can close this window and read the terminal.\n")
		return
	}
	code := query.Get("code")
	if code == "" {
		c.finish(callbackResult{Err: errors.New("the callback arrived without a code")})
		writeCallbackPage(w, http.StatusBadRequest,
			"atlas got a callback without a code in it.\nNothing was authorized. Start /mcp login again from the terminal.\n")
		return
	}
	c.finish(callbackResult{Code: code})
	writeCallbackPage(w, http.StatusOK, callbackPage)
}

// wait blocks until the browser arrives or the deadline passes.
//
// The timeout is a parameter rather than the context's deadline so that the
// message can name it: "no callback arrived" is only actionable with the number
// the reader was promised.
func (c *callbackServer) wait(ctx context.Context, timeout time.Duration) (string, error) {
	select {
	case result := <-c.result:
		return result.Code, result.Err
	case <-ctx.Done():
	}
	address := c.listener.Addr().String()
	port := address[strings.LastIndexByte(address, ':')+1:]
	return "", fmt.Errorf("no callback arrived on %s within %s; if the browser is on another machine, forward the port first: ssh -L %s:%s <this machine>", c.redirectURI(), timeout, port, address)
}

// finish hands the result to the waiter, once. A second callback — a reloaded
// page, a second tab — changes nothing.
func (c *callbackServer) finish(result callbackResult) {
	c.deliver.Do(func() { c.result <- result })
}

// Close stops the listener. It is deferred by a login that already returned, so it
// must be idempotent.
func (c *callbackServer) Close() {
	c.closing.Do(func() {
		_ = c.server.Close()
	})
}

// refusalError words the authorization server's refusal. The four codes a code
// flow can come back with mean different things to the reader, and none of them
// is visible in the code itself.
func refusalError(code, description string) error {
	reason := map[string]string{
		"access_denied":       "the authorization was declined, or nothing was granted",
		"invalid_scope":       "the server does not offer the scopes this server asked for",
		"invalid_request":     "the server rejected the request itself, which is a bug in this client",
		"unauthorized_client": "the server does not accept this client registration",
	}[code]
	message := "the authorization server refused the login: " + code
	if reason != "" {
		message += " (" + reason + ")"
	}
	if description != "" {
		message += ": " + description
	}
	return errors.New(message)
}

// writeCallbackPage answers the browser with one plain-text line.
func writeCallbackPage(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

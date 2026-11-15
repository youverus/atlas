package mcp

import (
	"fmt"
	"strings"
	"time"
)

// The protocol version this client asks for, and the ones it accepts back.
//
// The revision decides which state machine both sides run. Every version below
// has the initialize handshake and the session concept; the 2026-07-28 revision
// dropped both, so there is no subset of it this client could speak and no
// point listing it. Accepting an older answer is not the same as speaking an
// older dialect: the field list of the messages used here (initialize,
// tools/list, tools/call) did not change across these four.
const (
	protocolVersion = "2025-11-25"

	// clientName is what initialize reports. A server logs it and nothing
	// compares it, so it is the binary's name rather than a package's.
	clientName = "atlas"
)

// supportedVersions is the acceptance list, newest first. The ordering is only
// for the error message.
var supportedVersions = []string{
	protocolVersion,
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

// supports reports whether a server's answer is one this client can run.
func supports(version string) bool {
	for _, known := range supportedVersions {
		if version == known {
			return true
		}
	}
	return false
}

// Budgets.
const (
	// DefaultTimeout bounds one request when the config names none. It is an
	// idle budget rather than a total one: a call that keeps reporting progress
	// restarts it, so a slow tool survives and a stuck one is still cut off
	// (progress.go, docs/mcp.md section 4).
	DefaultTimeout = 60 * time.Second

	// handshakeTimeout bounds initialize plus tools/list. It is much shorter
	// than DefaultTimeout because it runs before the prompt appears: waiting a
	// minute at startup would look like a hang.
	handshakeTimeout = 20 * time.Second

	// maxListPages bounds the tools/list cursor loop. A server that keeps
	// handing back a cursor without making progress would otherwise spin until
	// the handshake timeout; the cap turns that into a message instead.
	maxListPages = 50
)

// Spec is one configured server reduced to what a connection needs.
//
// It is a plain value on purpose. The configuration package owns the file
// format, this package owns the protocol, and neither imports the other:
// cmd/atlas does the translation. That is also why the field names here are the
// protocol's words rather than the file's.
type Spec struct {
	// Name is the configured name. It prefixes every tool the server exposes
	// and appears in every message about the server.
	Name string

	// Command, Args, Env and Dir describe a stdio server. Env is added to the
	// inherited environment. Dir empty means the process's own directory.
	Command string
	Args    []string
	Env     map[string]string
	Dir     string

	// URL and Headers describe a streamable HTTP server. Headers carry the
	// credentials, so they are sent on every request, initialize included.
	URL     string
	Headers map[string]string

	// OAuth marks a server that authorizes with a token this client obtains
	// itself. Only meaningful together with URL: there is no HTTP endpoint in a
	// stdio server for a token to travel on. It is the fallback source for the
	// Authorization header, never a replacement for Headers.
	OAuth *OAuthSpec

	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration

	// Tools narrows the server's tool list to these names, as the server spells
	// them. Empty means every tool it declares. It is a whitelist rather than a
	// filter of prefixes because the names are the server's, and a list of the
	// ones a session wants is shorter to read than the ones it does not.
	Tools []string
}

// allows reports whether a tool the server declared belongs in this session.
func (s Spec) allows(name string) bool {
	if len(s.Tools) == 0 {
		return true
	}
	for _, allowed := range s.Tools {
		if allowed == name {
			return true
		}
	}
	return false
}

// newTransport starts the transport this spec describes.
//
// The config rejects a spec with both transports or neither, but this package
// is also reachable from tests, and a nil transport is a much later panic than a
// named error.
func (s Spec) newTransport() (transport, error) {
	switch {
	case s.Command != "" && s.URL != "":
		return nil, fmt.Errorf("server %q: set either \"command\" or \"url\", not both", s.Name)
	case s.Command != "":
		return startStdio(s)
	case s.URL != "":
		return newHTTP(s)
	default:
		return nil, fmt.Errorf("server %q: needs a \"command\" or a \"url\"", s.Name)
	}
}

// timeout returns the idle budget: how long one call may say nothing before it
// is abandoned. It is not a total, because a server that reports progress is
// working, not stuck.
func (s Spec) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// Identity is what this client calls itself in initialize.
type Identity struct {
	Name    string
	Version string
}

// Info is what a server said about itself during the handshake. It is kept for
// display: /mcp shows the version, because "the server is a different build than
// the one in the config" is otherwise invisible.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// versionError is the refusal for an answer this client does not speak. It is a
// distinct shape only so that the tests can assert on the list being printed
// without matching prose.
func versionError(name, version string) error {
	return fmt.Errorf("server %q speaks MCP %s, which this client does not; it speaks %s",
		name, version, strings.Join(supportedVersions, ", "))
}

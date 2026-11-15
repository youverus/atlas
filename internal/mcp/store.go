package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// OAuthFileName is the file under the atlas home that holds the authorizations.
// It lives next to mcp.json because the two are read together: the config says a
// server wants OAuth, and this file says whether it has it.
const OAuthFileName = "oauth.json"

// oauthFileVersion is this file's schema. Nothing reads it yet, which is the
// point of writing it: the second version needs a place to start deciding how to
// read the first, and a schema that was never declared has no way to be
// recognised later.
const oauthFileVersion = 1

// OAuthClient is one registration with one authorization server.
//
// Method is what the token endpoint expects, kept because guessing it later
// would mean guessing whether to send a secret in the body or in a header.
type OAuthClient struct {
	ID     string `json:"id"`
	Secret string `json:"secret,omitempty"`
	Method string `json:"method"`
}

// OAuthTokens is one authorization. ExpiresAt is informational: this client does
// not schedule a refresh, because a token response may omit expires_in and the
// one reliable signal is a 401 (docs/mcp-oauth.md section 5).
type OAuthTokens struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

// OAuthEndpoint is what the discovery found. Only the three fields a refresh
// needs are kept: the rest of the metadata would be one more thing to go stale,
// and a refresh that fails has to rediscover it anyway.
type OAuthEndpoint struct {
	AuthorizationServer string `json:"authorizationServer"`
	Token               string `json:"token"`
	Resource            string `json:"resource,omitempty"`
}

// OAuthEntry is everything kept for one server URL. The pointers are nil for the
// states in between: a registration without tokens is a login that did not
// finish, and tokens without a registration cannot happen.
type OAuthEntry struct {
	Client   *OAuthClient   `json:"client,omitempty"`
	Tokens   *OAuthTokens   `json:"tokens,omitempty"`
	Endpoint *OAuthEndpoint `json:"endpoint,omitempty"`
}

// authorized reports whether this entry can open a connection.
func (e OAuthEntry) authorized() bool {
	return e.Tokens != nil && e.Tokens.Access != ""
}

// oauthFile is the JSON shape of oauth.json.
type oauthFile struct {
	Version int                   `json:"version"`
	Servers map[string]OAuthEntry `json:"servers"`
}

// OAuthStore is oauth.json: what this client registered with each authorization
// server, and what the user authorized.
//
// The key is the server's URL rather than its configured name. Renaming a server
// must not throw an authorization away, and two servers that happen to share a
// name must never share a token. The doc calls this the only guarantee that a
// credential does not leak across servers, which is why the lookup is exact: no
// normalisation, no prefix match.
type OAuthStore struct {
	path string

	mu   sync.Mutex
	file oauthFile
}

// NewOAuthStore reads the store at path. A missing, unreadable or half-written
// file is an empty table rather than an error: an interrupted write must not cost
// a session its startup, and the next successful login writes the whole file
// again. The write path reports its own failures, which is the only place a user
// can act on them.
func NewOAuthStore(path string) *OAuthStore {
	store := &OAuthStore{
		path: path,
		file: oauthFile{Version: oauthFileVersion, Servers: map[string]OAuthEntry{}},
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return store
	}
	var file oauthFile
	if err := json.Unmarshal(encoded, &file); err != nil {
		return store
	}
	if file.Servers != nil {
		store.file.Servers = file.Servers
	}
	return store
}

// Path returns the file this store writes, for a message that has to name it.
func (s *OAuthStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Entry returns what is kept for one server URL, or a zero entry. A nil store is
// a valid receiver so that a session without OAuth pays no nil checks.
func (s *OAuthStore) Entry(serverURL string) OAuthEntry {
	if s == nil {
		return OAuthEntry{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Servers[serverURL]
}

// Put replaces the entry for one server URL and writes the file.
//
// The whole file is rewritten rather than edited in place: it is small, it is
// read and written from one process, and a rewrite is the only shape that can be
// made atomic.
func (s *OAuthStore) Put(serverURL string, entry OAuthEntry) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.file.Servers[serverURL] = entry
	return s.write()
}

// write encodes and replaces the file through a temporary one.
//
// The rename matters: this file is rewritten between turns, and a crash halfway
// through a direct write would leave JSON that the next start reads as an empty
// table — every authorization gone. Same directory, because rename is only atomic
// within one filesystem.
func (s *OAuthStore) write() error {
	encoded, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return fmt.Errorf("oauth: encode %s: %w", s.path, err)
	}
	encoded = append(encoded, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("oauth: %s: %w", s.path, err)
	}
	temp, err := os.CreateTemp(dir, OAuthFileName+".*")
	if err != nil {
		return fmt.Errorf("oauth: %s: %w", s.path, err)
	}
	name := temp.Name()
	// A successful rename moves the file; this removes the temporary one on
	// every path that did not get there.
	defer os.Remove(name)

	// The mode is set before the content is written: a file that becomes
	// readable only after the tokens are in it already has them.
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("oauth: %s: %w", s.path, err)
	}
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return fmt.Errorf("oauth: %s: %w", s.path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("oauth: %s: %w", s.path, err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("oauth: %s: %w", s.path, err)
	}
	return nil
}

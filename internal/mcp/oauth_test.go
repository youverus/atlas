package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAS is one server playing every part a login talks to: the MCP resource that
// answers 401, and the authorization server behind it. Each test overrides the
// paths it is about and reads seen for the ones it is not.
type fakeAS struct {
	mu      sync.Mutex
	seen    []string
	bodies  map[string]string
	handler func(f *fakeAS, w http.ResponseWriter, r *http.Request)
	server  *httptest.Server
}

func newFakeAS(t *testing.T, handler func(f *fakeAS, w http.ResponseWriter, r *http.Request)) *fakeAS {
	t.Helper()
	f := &fakeAS{handler: handler, bodies: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("read %s %s: %v", r.Method, r.URL.Path, err)
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+r.URL.Path)
		if len(body) > 0 {
			f.bodies[r.URL.Path] = string(body)
		}
		f.mu.Unlock()
		// The handler below reads the body itself, because that is what a real
		// server looks like.
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.handler(f, w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// requests is every request this server saw, in order.
func (f *fakeAS) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// count is how many requests reached one path.
func (f *fakeAS) count(path string) int {
	total := 0
	for _, request := range f.requests() {
		if strings.HasSuffix(request, " "+path) {
			total++
		}
	}
	return total
}

// body is the last body one path received.
func (f *fakeAS) body(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[path]
}

// serveOAuth is the authorization server every login test starts from: one that
// publishes both documents, registers this client, and grants a token. A test about
// a deviation calls it for the paths that are ordinary.
func serveOAuth(f *fakeAS, w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/mcp":
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer resource_metadata="%s%s/mcp", scope="read write"`, f.server.URL, protectedResourcePath))
		w.WriteHeader(http.StatusUnauthorized)
	case protectedResourcePath + "/mcp":
		writeDocument(w, map[string]any{
			"resource":              f.server.URL + "/mcp",
			"authorization_servers": []string{f.server.URL},
			"scopes_supported":      []string{"read", "write"},
		})
	case authorizationServerPath:
		writeDocument(w, map[string]any{
			"issuer":                                f.server.URL,
			"authorization_endpoint":                f.server.URL + "/authorize",
			"token_endpoint":                        f.server.URL + "/token",
			"registration_endpoint":                 f.server.URL + "/register",
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	case "/register":
		writeDocument(w, map[string]any{"client_id": "client-1", "token_endpoint_auth_method": "none"})
	case "/token":
		writeDocument(w, map[string]any{
			"access_token":  "access-1",
			"token_type":    "Bearer",
			"refresh_token": "refresh-1",
			"scope":         "read write",
		})
	default:
		http.NotFound(w, r)
	}
}

// writeDocument answers a metadata, registration or token request.
func writeDocument(w http.ResponseWriter, document any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(document); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// newLoginScene wires one spec to a store in a fresh temporary home.
func newLoginScene(t *testing.T, handler func(f *fakeAS, w http.ResponseWriter, r *http.Request)) (Spec, *OAuthStore, *fakeAS) {
	t.Helper()
	f := newFakeAS(t, handler)
	store := NewOAuthStore(filepath.Join(t.TempDir(), OAuthFileName))
	spec := Spec{Name: "remote", URL: f.server.URL + "/mcp", OAuth: &OAuthSpec{Store: store}}
	return spec, store, f
}

// answerCallback performs the browser's half: it reads the state out of the
// authorize URL and calls the loopback address with it. An override replaces one
// parameter; an empty value deletes it.
func answerCallback(t *testing.T, authorize, redirectURI string, override map[string]string) (int, string) {
	t.Helper()
	parsed, err := url.Parse(authorize)
	if err != nil {
		t.Fatalf("authorize url %q: %v", authorize, err)
	}
	query := url.Values{}
	query.Set("code", "code-1")
	query.Set("state", parsed.Query().Get("state"))
	for name, value := range override {
		if value == "" {
			query.Del(name)
			continue
		}
		query.Set(name, value)
	}
	response, err := http.Get(redirectURI + "?" + query.Encode())
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer response.Body.Close()
	page, err := io.ReadAll(io.LimitReader(response.Body, 1<<12))
	if err != nil {
		t.Fatalf("read the callback page: %v", err)
	}
	return response.StatusCode, string(page)
}

// loginPrompt answers the callback as soon as the URL is known.
//
// No goroutine is needed: the listener is already accepting and its result waits in
// a buffered channel, so Login blocks on a value that is already there.
func loginPrompt(t *testing.T) func(authorize, redirectURI string) {
	t.Helper()
	return func(authorize, redirectURI string) {
		if status, page := answerCallback(t, authorize, redirectURI, nil); status != http.StatusOK {
			t.Fatalf("the callback answered %d: %s", status, page)
		}
	}
}

// TestProtectedResourceMetadata is acceptance 1: the three addresses and the
// refusal when none of them answers.
func TestProtectedResourceMetadata(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	t.Run("the 401 names it", func(t *testing.T) {
		f := newFakeAS(t, func(f *fakeAS, w http.ResponseWriter, _ *http.Request) {
			writeDocument(w, map[string]any{"resource": f.server.URL + "/mcp"})
		})
		serverURL, err := url.Parse(f.server.URL + "/mcp")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		advertised := f.server.URL + protectedResourcePath + "/mcp"
		document, err := discoverResource(context.Background(), client, serverURL, advertised)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if document.Resource != f.server.URL+"/mcp" {
			t.Fatalf("resource = %q, want the server's own URL", document.Resource)
		}
	})

	t.Run("nothing advertised, the suffixed path answers", func(t *testing.T) {
		f := newFakeAS(t, func(f *fakeAS, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != protectedResourcePath+"/mcp" {
				http.NotFound(w, r)
				return
			}
			writeDocument(w, map[string]any{"resource": f.server.URL + "/mcp"})
		})
		serverURL, _ := url.Parse(f.server.URL + "/mcp")
		if _, err := discoverResource(context.Background(), client, serverURL, ""); err != nil {
			t.Fatalf("discover: %v", err)
		}
		if got := f.count(protectedResourcePath + "/mcp"); got != 1 {
			t.Fatalf("requests to the suffixed path = %d, want 1", got)
		}
	})

	t.Run("the suffixed path is missing, the bare one answers", func(t *testing.T) {
		f := newFakeAS(t, func(f *fakeAS, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != protectedResourcePath {
				http.NotFound(w, r)
				return
			}
			writeDocument(w, map[string]any{"resource": f.server.URL + "/mcp"})
		})
		serverURL, _ := url.Parse(f.server.URL + "/mcp")
		if _, err := discoverResource(context.Background(), client, serverURL, ""); err != nil {
			t.Fatalf("discover: %v", err)
		}
		if got := f.count(protectedResourcePath); got != 1 {
			t.Fatalf("requests to the bare path = %d, want the fallback", got)
		}
	})

	t.Run("both are missing", func(t *testing.T) {
		f := newFakeAS(t, func(_ *fakeAS, w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
		serverURL, _ := url.Parse(f.server.URL + "/mcp")
		_, err := discoverResource(context.Background(), client, serverURL, "")
		if err == nil {
			t.Fatal("discover succeeded, want a refusal")
		}
		for _, want := range []string{f.server.URL + protectedResourcePath + "/mcp", f.server.URL + protectedResourcePath} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want it to name %s", err, want)
			}
		}
	})
}

// TestIssuerMismatchStopsBeforeRegistering is acceptance 2: a document that does not
// name itself is refused before this client hands it anything.
func TestIssuerMismatchStopsBeforeRegistering(t *testing.T) {
	spec, _, f := newLoginScene(t, func(f *fakeAS, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case authorizationServerPath:
			writeDocument(w, map[string]any{
				"issuer":                 "https://other.example",
				"authorization_endpoint": f.server.URL + "/authorize",
				"token_endpoint":         f.server.URL + "/token",
				"registration_endpoint":  f.server.URL + "/register",
			})
		default:
			serveOAuth(f, w, r)
		}
	})

	_, err := Login(context.Background(), spec, func(string, string) {
		t.Error("the browser was opened before the metadata was checked")
	})
	if err == nil {
		t.Fatal("Login succeeded, want the issuer refusal")
	}
	if !strings.Contains(err.Error(), "other.example") {
		t.Fatalf("error = %v, want it to name the issuer the document claimed", err)
	}
	if got := f.count("/register"); got != 0 {
		t.Fatalf("registration requests = %d, want none", got)
	}
}

// TestRegistersOnce is acceptance 3: the registration carries what a server needs to
// allow a loopback redirect, and a second login reuses it instead of registering
// again.
func TestRegistersOnce(t *testing.T) {
	spec, store, f := newLoginScene(t, serveOAuth)

	result, err := Login(context.Background(), spec, loginPrompt(t))
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if got := f.count("/register"); got != 1 {
		t.Fatalf("registration requests = %d, want 1", got)
	}

	var registration struct {
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		ApplicationType         string   `json:"application_type"`
	}
	if err := json.Unmarshal([]byte(f.body("/register")), &registration); err != nil {
		t.Fatalf("registration body %q: %v", f.body("/register"), err)
	}
	// Without "native" a server defaults to "web", which may not use a loopback
	// redirect: the registration succeeds and the authorize request fails.
	if registration.ApplicationType != "native" {
		t.Fatalf("application_type = %q, want native", registration.ApplicationType)
	}
	for _, want := range []string{"authorization_code", "refresh_token"} {
		if !containsFold(registration.GrantTypes, want) {
			t.Fatalf("grant_types = %v, want %s in it", registration.GrantTypes, want)
		}
	}
	if !containsFold(registration.ResponseTypes, "code") {
		t.Fatalf("response_types = %v, want code", registration.ResponseTypes)
	}
	if len(registration.RedirectURIs) != 1 {
		t.Fatalf("redirect_uris = %v, want one", registration.RedirectURIs)
	}
	redirect := registration.RedirectURIs[0]
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, callbackPath) {
		t.Fatalf("redirect_uris[0] = %q, want a loopback address on 127.0.0.1", redirect)
	}
	if registration.TokenEndpointAuthMethod != authMethodNone {
		t.Fatalf("token_endpoint_auth_method = %q, want %q", registration.TokenEndpointAuthMethod, authMethodNone)
	}

	entry := store.Entry(spec.URL)
	if entry.Client == nil || entry.Client.ID != "client-1" {
		t.Fatalf("stored client = %+v, want client-1", entry.Client)
	}
	if entry.Tokens == nil || entry.Tokens.Access != "access-1" {
		t.Fatalf("stored tokens = %+v, want the granted one", entry.Tokens)
	}
	if result.Scopes != "read write" {
		t.Fatalf("scopes = %q, want the granted ones", result.Scopes)
	}

	if _, err := Login(context.Background(), spec, loginPrompt(t)); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if got := f.count("/register"); got != 1 {
		t.Fatalf("registration requests after a second login = %d, want the stored client reused", got)
	}
}

// TestPKCE is acceptance 4: the challenge, the verifier that has to match it, and
// the refusal of a server that offers only "plain".
func TestPKCE(t *testing.T) {
	spec, _, f := newLoginScene(t, serveOAuth)

	var challenge string
	if _, err := Login(context.Background(), spec, func(authorize, redirectURI string) {
		parsed, err := url.Parse(authorize)
		if err != nil {
			t.Fatalf("authorize url: %v", err)
		}
		challenge = parsed.Query().Get("code_challenge")
		answerCallback(t, authorize, redirectURI, nil)
	}); err != nil {
		t.Fatalf("login: %v", err)
	}

	authorize := f.body("/token")
	form, err := url.ParseQuery(authorize)
	if err != nil {
		t.Fatalf("token body %q: %v", authorize, err)
	}
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatal("the token request carried no code_verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != challenge {
		t.Fatalf("the verifier's S256 is %q, but the authorize URL carried %q", got, challenge)
	}

	t.Run("plain only", func(t *testing.T) {
		spec, _, _ := newLoginScene(t, func(f *fakeAS, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == authorizationServerPath {
				writeDocument(w, map[string]any{
					"issuer":                           f.server.URL,
					"authorization_endpoint":           f.server.URL + "/authorize",
					"token_endpoint":                   f.server.URL + "/token",
					"registration_endpoint":            f.server.URL + "/register",
					"code_challenge_methods_supported": []string{"plain"},
				})
				return
			}
			serveOAuth(f, w, r)
		})
		_, err := Login(context.Background(), spec, func(string, string) {
			t.Error("the browser was opened for a server that cannot do S256")
		})
		if err == nil || !strings.Contains(err.Error(), pkceMethod) {
			t.Fatalf("error = %v, want a refusal naming %s", err, pkceMethod)
		}
	})
}

// TestCallbackState is acceptance 5: the wrong state is refused and exchanges
// nothing, the right one is spent once, and a callback that never arrives ends the
// wait.
func TestCallbackState(t *testing.T) {
	t.Run("a wrong state is refused", func(t *testing.T) {
		spec, _, f := newLoginScene(t, serveOAuth)
		if _, err := Login(context.Background(), spec, func(authorize, redirectURI string) {
			status, page := answerCallback(t, authorize, redirectURI, map[string]string{"state": "not-the-one"})
			if status != http.StatusBadRequest {
				t.Fatalf("the wrong state answered %d, want 400: %s", status, page)
			}
			if !strings.Contains(page, "state") {
				t.Fatalf("page = %q, want it to say what was refused", page)
			}
			// The real callback is still expected: it was not refused.
			if status, page := answerCallback(t, authorize, redirectURI, nil); status != http.StatusOK {
				t.Fatalf("the real callback answered %d, want 200: %s", status, page)
			}
		}); err != nil {
			t.Fatalf("login: %v", err)
		}
		if got := f.count("/token"); got != 1 {
			t.Fatalf("token requests = %d, want the one the real callback earned", got)
		}
	})

	t.Run("the right state gets a page", func(t *testing.T) {
		spec, _, _ := newLoginScene(t, serveOAuth)
		if _, err := Login(context.Background(), spec, func(authorize, redirectURI string) {
			status, page := answerCallback(t, authorize, redirectURI, nil)
			if status != http.StatusOK {
				t.Fatalf("callback answered %d: %s", status, page)
			}
			if !strings.Contains(page, "close this window") {
				t.Fatalf("page = %q, want the one line a browser can act on", page)
			}
		}); err != nil {
			t.Fatalf("login: %v", err)
		}
	})

	t.Run("no callback times out", func(t *testing.T) {
		callback, err := startCallback("state-1", "https://auth.example")
		if err != nil {
			t.Fatalf("startCallback: %v", err)
		}
		defer callback.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err = callback.wait(ctx, 50*time.Millisecond)
		if err == nil {
			t.Fatal("wait returned a code, want the timeout")
		}
		if !strings.Contains(err.Error(), "no callback arrived") || !strings.Contains(err.Error(), "ssh -L") {
			t.Fatalf("error = %v, want the timeout and the way to forward the port", err)
		}
	})
}

// TestRefreshOnce is acceptance 6: one refresh, one retry, and a refusal that ends
// in "log in again" rather than in a loop.
func TestRefreshOnce(t *testing.T) {
	t.Run("a refused token is refreshed and retried", func(t *testing.T) {
		var mu sync.Mutex
		var authorizations []string
		f := newFakeAS(t, func(f *fakeAS, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				writeDocument(w, map[string]any{"access_token": "access-2", "token_type": "Bearer", "refresh_token": "refresh-2"})
				return
			}
			if r.Method == http.MethodGet {
				// No stream channel here: the handler below reads a JSON-RPC
				// body, and a GET has none.
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			mu.Lock()
			authorizations = append(authorizations, r.Header.Get("Authorization"))
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer access-2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var message incoming
			if err := json.Unmarshal(body, &message); err != nil {
				t.Errorf("decode %s: %v", body, err)
				return
			}
			switch message.Method {
			case "initialize":
				writeJSON(w, message.ID, map[string]any{
					"protocolVersion": protocolVersion,
					"serverInfo":      map[string]any{"name": "refreshable", "version": "1"},
				})
			case "tools/list":
				writeJSON(w, message.ID, map[string]any{"tools": []any{
					map[string]any{"name": "ping", "inputSchema": map[string]any{"type": "object"}},
				}})
			default:
				w.WriteHeader(http.StatusAccepted)
			}
		})

		store := NewOAuthStore(filepath.Join(t.TempDir(), OAuthFileName))
		if err := store.Put(f.server.URL, OAuthEntry{
			Client:   &OAuthClient{ID: "client-1", Method: authMethodNone},
			Tokens:   &OAuthTokens{Access: "access-1", Refresh: "refresh-1", Scope: "read"},
			Endpoint: &OAuthEndpoint{AuthorizationServer: f.server.URL, Token: f.server.URL + "/token", Resource: f.server.URL},
		}); err != nil {
			t.Fatalf("store: %v", err)
		}

		// The name is not the key: a renamed server keeps its authorization.
		set := Open(context.Background(), []Spec{{
			Name:  "renamed",
			URL:   f.server.URL,
			OAuth: &OAuthSpec{Store: store},
		}}, Identity{Name: "atlas", Version: "test"})
		t.Cleanup(set.Close)

		if err := set.Servers()[0].Err; err != nil {
			t.Fatalf("connect: %v", err)
		}
		if got := len(set.Servers()[0].Tools); got != 1 {
			t.Fatalf("tools = %d, want the one the second attempt returned", got)
		}
		if got := f.count("/token"); got != 1 {
			t.Fatalf("token requests = %d, want 1", got)
		}
		form, err := url.ParseQuery(f.body("/token"))
		if err != nil {
			t.Fatalf("token body: %v", err)
		}
		if got := form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", got)
		}
		if got := form.Get("refresh_token"); got != "refresh-1" {
			t.Fatalf("refresh_token = %q, want the stored one", got)
		}
		if got := form.Get("resource"); got != f.server.URL {
			t.Fatalf("resource = %q, want the stored audience", got)
		}

		mu.Lock()
		seen := append([]string(nil), authorizations...)
		mu.Unlock()
		if len(seen) < 2 || seen[0] != "Bearer access-1" {
			t.Fatalf("authorizations = %v, want the first attempt on the stored token", seen)
		}
		for index, value := range seen[1:] {
			if value != "Bearer access-2" {
				t.Fatalf("attempt %d carried %q, want the refreshed token", index+2, value)
			}
		}

		stored := store.Entry(f.server.URL)
		if stored.Tokens == nil || stored.Tokens.Access != "access-2" || stored.Tokens.Refresh != "refresh-2" {
			t.Fatalf("stored tokens = %+v, want the refreshed pair written down", stored.Tokens)
		}
		if stored.Tokens.Scope != "read" {
			t.Fatalf("scope = %q, want the old one kept: the answer named none", stored.Tokens.Scope)
		}
	})

	t.Run("invalid_grant asks for a login", func(t *testing.T) {
		f := newFakeAS(t, func(f *fakeAS, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				writeDocument(w, map[string]any{"error": "invalid_grant", "error_description": "the refresh token was revoked"})
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		})

		store := NewOAuthStore(filepath.Join(t.TempDir(), OAuthFileName))
		if err := store.Put(f.server.URL, OAuthEntry{
			Client:   &OAuthClient{ID: "client-1", Method: authMethodNone},
			Tokens:   &OAuthTokens{Access: "access-1", Refresh: "refresh-1"},
			Endpoint: &OAuthEndpoint{AuthorizationServer: f.server.URL, Token: f.server.URL + "/token"},
		}); err != nil {
			t.Fatalf("store: %v", err)
		}

		set := Open(context.Background(), []Spec{{
			Name:  "remote",
			URL:   f.server.URL,
			OAuth: &OAuthSpec{Store: store},
		}}, Identity{Name: "atlas", Version: "test"})
		t.Cleanup(set.Close)

		err := set.Servers()[0].Err
		if err == nil {
			t.Fatal("connect succeeded, want the refusal")
		}
		if !strings.Contains(err.Error(), "/mcp login") || !strings.Contains(err.Error(), "invalid_grant") {
			t.Fatalf("error = %v, want the way back and the reason", err)
		}
		if got := f.count("/token"); got != 1 {
			t.Fatalf("token requests = %d, want exactly one attempt", got)
		}
		if got := f.count("/"); got != 1 {
			t.Fatalf("requests to the server = %d, want no retry after a failed refresh", got)
		}
	})
}

// TestOAuthStore is acceptance 7: the file's permissions, two URLs that do not
// share anything, and a half-written file that is read as an empty table.
func TestOAuthStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), OAuthFileName)
	store := NewOAuthStore(path)
	put := func(serverURL, access string) {
		t.Helper()
		if err := store.Put(serverURL, OAuthEntry{Tokens: &OAuthTokens{Access: access}}); err != nil {
			t.Fatalf("put %s: %v", serverURL, err)
		}
	}
	put("https://a.example/mcp", "token-a")
	put("https://b.example/mcp", "token-b")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600: the file holds tokens", got)
	}

	// Two URLs, two entries, and no third.
	reopened := NewOAuthStore(path)
	for serverURL, want := range map[string]string{
		"https://a.example/mcp": "token-a",
		"https://b.example/mcp": "token-b",
	} {
		entry := reopened.Entry(serverURL)
		if entry.Tokens == nil || entry.Tokens.Access != want {
			t.Fatalf("entry for %s = %+v, want %s", serverURL, entry.Tokens, want)
		}
	}
	if entry := reopened.Entry("https://c.example/mcp"); entry.authorized() {
		t.Fatal("an unknown URL produced an authorization")
	}

	// A half-written file is a truncated write, not a broken start.
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":{"https://a.exa`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	broken := NewOAuthStore(path)
	if broken.Entry("https://a.example/mcp").authorized() {
		t.Fatal("a truncated file produced a token")
	}
	if err := broken.Put("https://d.example/mcp", OAuthEntry{Tokens: &OAuthTokens{Access: "token-d"}}); err != nil {
		t.Fatalf("the store does not recover: %v", err)
	}
	if entry := NewOAuthStore(path).Entry("https://d.example/mcp"); !entry.authorized() {
		t.Fatal("the recovered store did not keep what it wrote")
	}
}

// TestOAuthServerWithoutTokenDoesNotConnect is acceptance 8: a server with no stored
// token costs no request, keeps its place in the table, and leaves the session
// otherwise untouched.
func TestOAuthServerWithoutTokenDoesNotConnect(t *testing.T) {
	f := newFakeAS(t, serveOAuth)
	store := NewOAuthStore(filepath.Join(t.TempDir(), OAuthFileName))
	set := Open(context.Background(), []Spec{{
		Name:  "linear",
		URL:   f.server.URL + "/mcp",
		OAuth: &OAuthSpec{Scopes: []string{"read"}, Store: store},
	}}, Identity{Name: "atlas", Version: "test"})
	t.Cleanup(set.Close)

	server := set.Servers()[0]
	if server.Err != nil {
		t.Fatalf("connect: %v, want no attempt at all", server.Err)
	}
	if !server.NeedsLogin {
		t.Fatal("NeedsLogin = false, want the server marked")
	}
	if server.Connected() {
		t.Fatal("Connected = true, want false")
	}
	if got := f.requests(); len(got) != 0 {
		t.Fatalf("requests = %v, want none while the token is missing", got)
	}
	if tools := set.Tools(); len(tools) != 0 {
		t.Fatalf("tools = %d, want none", len(tools))
	}
	notes := set.Notes()
	if len(notes) != 1 || !strings.Contains(notes[0], "/mcp login linear") {
		t.Fatalf("notes = %v, want the one line that names the command", notes)
	}
}

// TestParseChallenge covers the header shapes the discovery depends on: the two
// quoting styles, the two schemes, and a challenge that is not this client's.
func TestParseChallenge(t *testing.T) {
	for _, tc := range []struct {
		name         string
		header       string
		wantResource string
		wantScope    string
	}{
		{
			name:         "quoted",
			header:       `Bearer resource_metadata="https://a.example/prm", scope="read write"`,
			wantResource: "https://a.example/prm",
			wantScope:    "read write",
		},
		{
			name:         "unquoted",
			header:       `Bearer resource_metadata=https://a.example/prm`,
			wantResource: "https://a.example/prm",
		},
		{
			name:         "dpop",
			header:       `DPoP resource_metadata="https://a.example/prm"`,
			wantResource: "https://a.example/prm",
		},
		{
			name:   "another scheme",
			header: `Basic realm="x"`,
		},
		{
			name:   "no header",
			header: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			challenge := parseChallenge(tc.header)
			if challenge.ResourceMetadata != tc.wantResource {
				t.Fatalf("resource_metadata = %q, want %q", challenge.ResourceMetadata, tc.wantResource)
			}
			if challenge.Scope != tc.wantScope {
				t.Fatalf("scope = %q, want %q", challenge.Scope, tc.wantScope)
			}
		})
	}
}

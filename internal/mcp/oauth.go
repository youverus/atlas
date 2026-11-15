package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Budgets and names of the authorization flow.
const (
	// LoginTimeout bounds a whole login, and almost all of it is the human part:
	// switching to a browser, maybe signing in, and coming back.
	LoginTimeout = 5 * time.Minute

	// oauthHTTPTimeout bounds one request to an authorization server. Unlike the
	// MCP transport, these requests are small and none of them streams, so a
	// plain client timeout is the right shape.
	oauthHTTPTimeout = 30 * time.Second

	// The two token endpoint authentication methods this client speaks. A
	// native client normally gets "none"; servers that hand out a secret use the
	// basic header.
	authMethodNone  = "none"
	authMethodBasic = "client_secret_basic"

	// pkceMethod is the only challenge method this client sends. RFC 7636
	// defines "plain" too, and it is refused rather than fallen back to: with
	// plain, the verifier travels in the authorize URL.
	pkceMethod = "S256"
)

// errReauthorize is a token the server refused that this session could not renew.
// It is a distinct value because it is the one authorization failure a user has
// to act on: no retry inside this session can produce a new one.
var errReauthorize = errors.New(`the server refused this session's token and it could not be renewed; run "/mcp login <server>" to authorize this server again`)

// OAuthSpec is how one server authorizes.
//
// It is a pointer on Spec so that "this server uses OAuth" and "this server does
// not" are the same question as "is there a store to read" — a zero OAuthSpec
// would otherwise have to be told apart from an absent one at every use.
type OAuthSpec struct {
	// Scopes is what the configuration asked for. Empty means "whatever the
	// server says it needs": a login request that names no scope is not a
	// request for none.
	Scopes []string
	// Store holds the registration and the tokens. A nil store authorizes but
	// cannot remember, which is only useful in tests.
	Store *OAuthStore
}

// scope returns the space-joined scope this configuration asks for.
func (o *OAuthSpec) scope() string {
	if o == nil {
		return ""
	}
	return strings.Join(o.Scopes, " ")
}

// authorized reports whether this server can be connected: a stored access token
// is the whole test. It does not check the expiry, because a token response may
// carry no expires_in and the reliable signal is a 401.
func (o *OAuthSpec) authorized(serverURL string) bool {
	if o == nil {
		return false
	}
	return o.Store.Entry(serverURL).authorized()
}

// LoginResult is what a completed login produced, for the line the user reads.
type LoginResult struct {
	// Scopes is what the server granted. Empty when neither the answer nor the
	// request named any.
	Scopes string
	// RedirectURI is the address the callback arrived on. It is reported
	// because it is the one thing that has to be forwarded when the browser is
	// not on this machine.
	RedirectURI string
}

// Login runs the authorization code flow for one server and stores the result.
//
// prompt is called with the address to open, before the wait begins, and may be
// nil. Everything up to the callback is bounded by LoginTimeout: the flow has to
// end on its own even when nobody is at the browser.
//
// The shape is the spec's, in the order the documents have to be read: a 401 to
// learn where the resource metadata lives, the metadata to learn the issuer, the
// issuer's document to learn the endpoints, one registration, then the browser.
func Login(ctx context.Context, spec Spec, prompt func(authorize, redirectURI string)) (LoginResult, error) {
	if spec.OAuth == nil {
		return LoginResult{}, fmt.Errorf("server %q is not configured to log in: it needs \"auth\": \"oauth\"", spec.Name)
	}
	serverURL, err := url.Parse(spec.URL)
	if err != nil || serverURL.Host == "" {
		return LoginResult{}, fmt.Errorf("server %q: %q is not an http address to discover an authorization server for", spec.Name, spec.URL)
	}
	ctx, cancel := context.WithTimeout(ctx, LoginTimeout)
	defer cancel()

	hc := &http.Client{Timeout: oauthHTTPTimeout}

	// The only place resource_metadata appears is the header of a refusal, so the
	// flow starts by provoking one.
	challenge, err := probeServer(ctx, hc, spec)
	if err != nil {
		return LoginResult{}, err
	}
	prm, err := discoverResource(ctx, hc, serverURL, challenge.ResourceMetadata)
	if err != nil {
		return LoginResult{}, err
	}
	resource, err := selectResource(prm, serverURL)
	if err != nil {
		return LoginResult{}, err
	}
	issuer, err := prm.authServer()
	if err != nil {
		return LoginResult{}, err
	}
	metadata, err := discoverAuthServer(ctx, hc, issuer)
	if err != nil {
		return LoginResult{}, err
	}
	if err := checkPKCE(metadata); err != nil {
		return LoginResult{}, err
	}
	scope := spec.OAuth.scope()
	if scope == "" && strings.TrimSpace(challenge.Scope) != "" {
		scope = strings.Join(strings.Fields(challenge.Scope), " ")
	}

	// The listener comes before the registration: a redirect URI is registered,
	// not amended, so the port has to exist before it is named to the server.
	verifier, err := newVerifier()
	if err != nil {
		return LoginResult{}, err
	}
	state, err := newState()
	if err != nil {
		return LoginResult{}, err
	}
	callback, err := startCallback(state, metadata.Issuer)
	if err != nil {
		return LoginResult{}, err
	}
	defer callback.Close()
	redirectURI := callback.redirectURI()

	client, err := clientFor(ctx, hc, spec.OAuth.Store, spec.URL, metadata, endpointFor(metadata, resource), redirectURI, scope)
	if err != nil {
		return LoginResult{}, err
	}
	authorize, err := buildAuthorizeURL(metadata, client, redirectURI, challengeFor(verifier), state, scope, resource)
	if err != nil {
		return LoginResult{}, err
	}
	if prompt != nil {
		prompt(authorize, redirectURI)
	}

	code, err := callback.wait(ctx, LoginTimeout)
	if err != nil {
		return LoginResult{}, err
	}
	endpoint := endpointFor(metadata, resource)
	tokens, err := exchangeCode(ctx, hc, endpoint, client, code, verifier, redirectURI)
	if oauthCode(err) == "invalid_client" {
		// The server no longer knows this registration: it was deleted, or the
		// secret rotated. An invalid_client refusal never reached the grant, so
		// this code is still good — registering again and spending it is one
		// retry, not a second browser round trip.
		client, err = registerClient(ctx, hc, metadata, redirectURI, scope)
		if err != nil {
			return LoginResult{}, fmt.Errorf("%w (registering again failed too)", err)
		}
		tokens, err = exchangeCode(ctx, hc, endpoint, client, code, verifier, redirectURI)
	}
	if err != nil {
		return LoginResult{}, err
	}
	if tokens.Scope == "" {
		// RFC 6749 section 5.1: an answer that names no scope granted the one that
		// was asked for, so the old value is not a guess.
		tokens.Scope = scope
	}

	entry := spec.OAuth.Store.Entry(spec.URL)
	entry.Client = client
	entry.Endpoint = endpoint
	entry.Tokens = &tokens
	if err := spec.OAuth.Store.Put(spec.URL, entry); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Scopes: tokens.Scope, RedirectURI: redirectURI}, nil
}

// probeServer sends one initialize without a token.
//
// A 200 is a refusal of a different kind: it says the server needs no
// authorization at all, which makes "auth": "oauth" a mistake in the config rather
// than a state to log in from. Saying so beats registering a client that will
// never be used.
func probeServer(ctx context.Context, hc *http.Client, spec Spec) (authChallenge, error) {
	params, err := describeParams(initializeParams{
		ProtocolVersion: protocolVersion,
		ClientInfo:      Identity{Name: clientName, Version: "0"},
	})
	if err != nil {
		return authChallenge{}, err
	}
	encoded, err := json.Marshal(rpcRequest{JSONRPC: jsonrpcVersion, ID: 1, Method: "initialize", Params: params})
	if err != nil {
		return authChallenge{}, fmt.Errorf("server %q: encode initialize: %w", spec.Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.URL, strings.NewReader(string(encoded)))
	if err != nil {
		return authChallenge{}, fmt.Errorf("server %q: %w", spec.Name, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range spec.Headers {
		request.Header.Set(name, value)
	}
	response, err := hc.Do(request)
	if err != nil {
		return authChallenge{}, fmt.Errorf("server %q: %w", spec.Name, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))

	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return parseChallenge(response.Header.Get("WWW-Authenticate")), nil
	case response.StatusCode == http.StatusOK:
		if request.Header.Get("Authorization") != "" {
			// The configured headers already authenticate: the server answered,
			// so there is nothing to log in to.
			return authChallenge{}, fmt.Errorf("server %q accepted a request that carried the configured headers, so it does not need a login; remove \"auth\": \"oauth\" from it", spec.Name)
		}
		return authChallenge{}, fmt.Errorf("server %q answered without asking for credentials, so it does not need a login; remove \"auth\": \"oauth\" from it", spec.Name)
	default:
		return authChallenge{}, fmt.Errorf("server %q answered %s to the initialize this login starts with", spec.Name, response.Status)
	}
}

// checkPKCE refuses a server that cannot do S256.
func checkPKCE(metadata authServerMetadata) error {
	supported := metadata.CodeChallengeMethodsSupported
	if len(supported) == 0 {
		// The field is optional, and its absence is not a claim that plain is the
		// only method. S256 is what this client sends either way.
		return nil
	}
	for _, method := range supported {
		if strings.EqualFold(method, pkceMethod) {
			return nil
		}
	}
	return fmt.Errorf("this authorization server offers %s for PKCE and not %s; atlas sends %s only, because \"plain\" puts the verifier itself in the URL", strings.Join(supported, ", "), pkceMethod, pkceMethod)
}

// endpointFor is what a refresh needs, and nothing else.
func endpointFor(metadata authServerMetadata, resource string) *OAuthEndpoint {
	return &OAuthEndpoint{
		AuthorizationServer: metadata.Issuer,
		Token:               metadata.TokenEndpoint,
		Resource:            resource,
	}
}

// clientFor returns the stored registration, or registers a new one.
//
// A registration is kept as soon as it is made: a login abandoned after this
// point must not leave a client at the authorization server that the next attempt
// knows nothing about.
func clientFor(ctx context.Context, hc *http.Client, store *OAuthStore, serverURL string, metadata authServerMetadata, endpoint *OAuthEndpoint, redirectURI, scope string) (*OAuthClient, error) {
	entry := store.Entry(serverURL)
	if entry.Client != nil && entry.Client.ID != "" {
		return entry.Client, nil
	}
	client, err := registerClient(ctx, hc, metadata, redirectURI, scope)
	if err != nil {
		return nil, err
	}
	entry.Client = client
	entry.Endpoint = endpoint
	if err := store.Put(serverURL, entry); err != nil {
		return nil, err
	}
	return client, nil
}

// registrationRequest is the RFC 7591 body. The members that are not optional by
// the RFC are sent even when they carry the only value the spec defines.
type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ApplicationType         string   `json:"application_type,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
}

// registrationResponse is the answer, plus the error members: a registration that
// fails answers the same content type with a refusal instead.
type registrationResponse struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret"`
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

// registerClient registers this client with one authorization server.
func registerClient(ctx context.Context, hc *http.Client, metadata authServerMetadata, redirectURI, scope string) (*OAuthClient, error) {
	if metadata.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("the authorization server at %s publishes no \"registration_endpoint\" and atlas has no pre-registered client, so there is no way to obtain one", metadata.Issuer)
	}
	method, err := pickClientAuthMethod(metadata)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(registrationRequest{
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: method,
		// OIDC's registration extension, and it is not optional in practice: a
		// server that does not see it defaults to "web", and a web client may not
		// use a loopback redirect. The registration then succeeds and the
		// authorize request fails with a message about the redirect URI.
		ApplicationType: "native",
		ClientName:      clientName,
		Scope:           scope,
	})
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", metadata.RegistrationEndpoint, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := hc.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", metadata.RegistrationEndpoint, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthDocumentBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", metadata.RegistrationEndpoint, err)
	}
	var answer registrationResponse
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, fmt.Errorf("%s answered %s with something that is not a registration: %q", metadata.RegistrationEndpoint, response.Status, clamp(string(payload), 120))
	}
	if answer.Error != "" {
		return nil, fmt.Errorf("the authorization server refused to register this client: %s%s", answer.Error, describeCause(answer.ErrorDescription))
	}
	if answer.ClientID == "" {
		return nil, fmt.Errorf("%s answered without a client_id", metadata.RegistrationEndpoint)
	}
	client := &OAuthClient{ID: answer.ClientID, Secret: answer.ClientSecret, Method: method}
	if answer.TokenEndpointAuthMethod != "" {
		// The registration answer is the truth about what this client is.
		client.Method = answer.TokenEndpointAuthMethod
	}
	if client.Method == authMethodBasic && client.Secret == "" {
		return nil, fmt.Errorf("the authorization server registered %q without a client secret, which %s needs", client.ID, authMethodBasic)
	}
	return client, nil
}

// pickClientAuthMethod chooses how the token endpoint will authenticate this
// client. Only two of the three methods RFC 8414 names are spoken.
func pickClientAuthMethod(metadata authServerMetadata) (string, error) {
	supported := metadata.TokenEndpointAuthMethodsSupported
	if len(supported) == 0 {
		// No list: "none" is what a native client asks for, and it is the method
		// the MCP specification assumes for one.
		return authMethodNone, nil
	}
	if containsFold(supported, authMethodNone) {
		return authMethodNone, nil
	}
	if containsFold(supported, authMethodBasic) {
		return authMethodBasic, nil
	}
	if containsFold(supported, "client_secret_post") {
		return "", errors.New("this authorization server only accepts the client secret in the request body (client_secret_post), which atlas does not do")
	}
	return "", fmt.Errorf("this authorization server offers %s for the token endpoint, none of which atlas speaks", strings.Join(supported, ", "))
}

// buildAuthorizeURL is the address the user opens.
func buildAuthorizeURL(metadata authServerMetadata, client *OAuthClient, redirectURI, challenge, state, scope, resource string) (string, error) {
	parsed, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("%s: %w", metadata.AuthorizationEndpoint, err)
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", client.ID)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", pkceMethod)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	if scope != "" {
		query.Set("scope", scope)
	}
	if resource != "" {
		// RFC 8707: the audience is asked for at authorize time as well as at
		// token time. A server that only checks the second one still gets it.
		query.Set("resource", resource)
	}
	if hasScope(scope, "offline_access") {
		// A refresh token is only issued for offline access, and a second consent
		// screen is what buys one. Without it, the next start needs the browser
		// again.
		query.Set("prompt", "consent")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// exchangeCode trades the code the browser brought back for tokens.
func exchangeCode(ctx context.Context, hc *http.Client, endpoint *OAuthEndpoint, client *OAuthClient, code, verifier, redirectURI string) (OAuthTokens, error) {
	if client == nil || endpoint == nil {
		return OAuthTokens{}, errors.New("the login has no registration to exchange a code with")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	if endpoint.Resource != "" {
		form.Set("resource", endpoint.Resource)
	}
	return tokenRequest(ctx, hc, endpoint.Token, *client, form)
}

// refreshTokens trades a refresh token for a new access token.
func refreshTokens(ctx context.Context, hc *http.Client, entry OAuthEntry, previous OAuthTokens) (OAuthTokens, error) {
	if entry.Endpoint == nil || entry.Endpoint.Token == "" {
		return OAuthTokens{}, errors.New("the stored authorization has no token endpoint")
	}
	if entry.Client == nil || entry.Client.ID == "" {
		return OAuthTokens{}, errors.New("the stored authorization has no client registration")
	}
	if previous.Refresh == "" {
		return OAuthTokens{}, errors.New("the stored authorization has no refresh token")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", previous.Refresh)
	if entry.Endpoint.Resource != "" {
		form.Set("resource", entry.Endpoint.Resource)
	}
	tokens, err := tokenRequest(ctx, hc, entry.Endpoint.Token, *entry.Client, form)
	if err != nil {
		return OAuthTokens{}, err
	}
	// RFC 6749 section 6: a refresh may narrow the scope, and an answer that
	// omits it means "unchanged". Carrying the old one forward is what stops a
	// plain refresh from quietly dropping permissions.
	if tokens.Scope == "" {
		tokens.Scope = previous.Scope
	}
	// Same rule for the refresh token: a server that does not rotate it does not
	// send it again, and dropping it would end the chain.
	if tokens.Refresh == "" {
		tokens.Refresh = previous.Refresh
	}
	return tokens, nil
}

// refreshAccess replaces the tokens of one server and writes them down.
//
// The write is part of the operation rather than a later convenience: a session
// that refreshed in memory only would hand the server's rotated refresh token
// nowhere, and the next start would present a token the server has already
// invalidated.
func refreshAccess(ctx context.Context, spec *OAuthSpec, serverURL string, previous OAuthTokens) (OAuthTokens, error) {
	entry := spec.Store.Entry(serverURL)
	hc := &http.Client{Timeout: oauthHTTPTimeout}
	refreshed, err := refreshTokens(ctx, hc, entry, previous)
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("%w (%s)", errReauthorize, err)
	}
	stored := entry
	stored.Tokens = &refreshed
	if err := spec.Store.Put(serverURL, stored); err != nil {
		return OAuthTokens{}, fmt.Errorf("the token was refreshed but could not be saved: %w", err)
	}
	return refreshed, nil
}

// tokenResponse is the RFC 6749 answer, with the error members: a refusal may
// arrive with 200, so the body is read before the status.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
}

// tokenRequest sends one form to the token endpoint.
func tokenRequest(ctx context.Context, hc *http.Client, endpoint string, client OAuthClient, form url.Values) (OAuthTokens, error) {
	if endpoint == "" {
		return OAuthTokens{}, errors.New("the authorization server names no token endpoint")
	}
	form.Set("client_id", client.ID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("%s: %w", endpoint, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	switch client.Method {
	case authMethodNone, "":
	case authMethodBasic:
		// RFC 6749 section 2.3.1: both halves are form-encoded before they are
		// joined, because either may contain a colon.
		request.SetBasicAuth(url.QueryEscape(client.ID), url.QueryEscape(client.Secret))
	default:
		return OAuthTokens{}, fmt.Errorf("the client registration says %q, which atlas does not speak", client.Method)
	}

	response, err := hc.Do(request)
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("%s: %w", endpoint, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthDocumentBytes))
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("%s: %w", endpoint, err)
	}

	var answer tokenResponse
	if err := json.Unmarshal(payload, &answer); err != nil {
		if response.StatusCode != http.StatusOK {
			return OAuthTokens{}, fmt.Errorf("%s answered %s", endpoint, response.Status)
		}
		return OAuthTokens{}, fmt.Errorf("%s answered with something that is not a token response: %q", endpoint, clamp(string(payload), 120))
	}
	if answer.Error != "" {
		return OAuthTokens{}, &oauthError{
			Code:        answer.Error,
			Description: answer.ErrorDescription,
			URI:         answer.ErrorURI,
			Status:      response.StatusCode,
		}
	}
	if response.StatusCode != http.StatusOK {
		return OAuthTokens{}, fmt.Errorf("%s answered %s", endpoint, response.Status)
	}
	if answer.AccessToken == "" {
		return OAuthTokens{}, fmt.Errorf("%s answered without an access token", endpoint)
	}

	tokens := OAuthTokens{Access: answer.AccessToken, Refresh: answer.RefreshToken, Scope: answer.Scope}
	if answer.ExpiresIn > 0 {
		// Kept for a reader of the file. Nothing schedules a refresh from it:
		// a token response may omit it, so the one signal this client acts on is
		// a 401 (docs/mcp-oauth.md section 5).
		tokens.ExpiresAt = time.Now().Add(time.Duration(answer.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return tokens, nil
}

// oauthError is an authorization server refusing a request.
//
// It is a type rather than a value because the code decides what happens next:
// invalid_client means the registration is gone, invalid_grant means the tokens
// are, and the two lead to different repairs.
type oauthError struct {
	Code        string
	Description string
	URI         string
	Status      int
}

func (e *oauthError) Error() string {
	message := "the authorization server refused the request: " + e.Code
	if e.Description != "" {
		message += ": " + e.Description
	}
	if e.Status != 0 && e.Status != http.StatusOK && e.Status != http.StatusBadRequest {
		message += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	return message
}

// oauthCode returns the error code an authorization server answered with, which
// is empty for every other kind of failure.
func oauthCode(err error) string {
	var refusal *oauthError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

// newVerifier makes a PKCE code verifier: 32 random bytes, base64url, which is the
// 43 characters RFC 7636 requires at minimum.
func newVerifier() (string, error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// challengeFor is the S256 challenge of a verifier.
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newState is the value that binds a callback to the login waiting for it. 16
// bytes rather than 32: it appears in a URL a person may read out.
func newState() (string, error) {
	raw, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomBytes(size int) ([]byte, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return nil, fmt.Errorf("the login needs randomness: %w", err)
	}
	return buffer, nil
}

// hasScope reports whether one space-separated list contains a scope.
func hasScope(scope, want string) bool {
	for _, field := range strings.Fields(scope) {
		if field == want {
			return true
		}
	}
	return false
}

// containsFold reports whether a list contains a value, ignoring case: these are
// registered identifiers, and a server that spells one differently still means it.
func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// describeCause words an optional description into a sentence tail.
func describeCause(description string) string {
	if description == "" {
		return ""
	}
	return ": " + description
}

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Addresses and budgets of the two discoveries.
const (
	// protectedResourcePath is the RFC 9728 well-known prefix. The server's own
	// path is appended to it, and the bare form is the fallback: a server at
	// https://example.com/mcp may publish under either.
	protectedResourcePath = "/.well-known/oauth-protected-resource"
	// authorizationServerPath is the RFC 8414 prefix, and oidcConfigurationPath
	// is the OIDC one. Both are tried, in that order, because a server that
	// publishes both publishes the OAuth one as the current shape.
	authorizationServerPath  = "/.well-known/oauth-authorization-server"
	oidcConfigurationPath    = "/.well-known/openid-configuration"
	maxOAuthDocumentBytes    = 1 << 20
	authorizationServerDocID = "authorization server metadata"
)

// authChallenge is what a 401 said about the authorization in front of it.
//
// Only the four parameters this client acts on are read. Everything else in the
// header is either for a browser or for a client that speaks more of OAuth than
// this one does.
type authChallenge struct {
	ResourceMetadata string
	Scope            string
	Error            string
	ErrorDescription string
}

// parseChallenge reads a WWW-Authenticate value.
//
// Any scheme other than bearer or dpop is treated as no challenge at all: those
// are the two the MCP specification defines, and reading a Negotiate header's
// parameters as OAuth's would make a proxy's challenge look like a server that
// wants a token.
func parseChallenge(header string) authChallenge {
	scheme, rest := cutToken(strings.TrimSpace(header))
	if !strings.EqualFold(scheme, "bearer") && !strings.EqualFold(scheme, "dpop") {
		return authChallenge{}
	}
	params := challengeParams(rest)
	return authChallenge{
		ResourceMetadata: params["resource_metadata"],
		Scope:            params["scope"],
		Error:            params["error"],
		ErrorDescription: params["error_description"],
	}
}

// cutToken splits the leading token from the rest of a header value.
func cutToken(header string) (string, string) {
	index := strings.IndexAny(header, " \t")
	if index < 0 {
		return header, ""
	}
	return header[:index], strings.TrimSpace(header[index+1:])
}

// challengeParams reads the auth-param list: name=value pairs, comma separated,
// with values optionally quoted.
//
// A quoted value may contain a comma, so the list cannot be split on commas
// first. An unterminated quote ends the parse rather than guessing where the
// value stopped, because the guess would be the input to a request.
func challengeParams(text string) map[string]string {
	params := map[string]string{}
	for {
		text = strings.TrimLeft(text, " \t,")
		if text == "" {
			return params
		}
		equals := strings.IndexByte(text, '=')
		if equals < 0 {
			return params
		}
		name := strings.ToLower(strings.TrimSpace(text[:equals]))
		text = strings.TrimLeft(text[equals+1:], " \t")
		var value string
		if strings.HasPrefix(text, `"`) {
			text = text[1:]
			end := strings.IndexByte(text, '"')
			if end < 0 {
				return params
			}
			value, text = text[:end], text[end+1:]
		} else {
			end := strings.IndexAny(text, " \t,")
			if end < 0 {
				value, text = text, ""
			} else {
				value, text = text[:end], text[end:]
			}
		}
		params[name] = value
	}
}

// resourceMetadata is the RFC 9728 document: what this resource is called, who
// issues tokens for it, and which scopes it knows about.
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServer returns the authorization server to use.
//
// The first entry wins when there are several. Nothing in the document says which
// one applies to which user or which region, so picking by any rule would be
// picking by a guess (docs/mcp-oauth.md section 8).
func (m resourceMetadata) authServer() (string, error) {
	for _, candidate := range m.AuthorizationServers {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", errors.New(`the protected resource metadata names no "authorization_servers", so there is no authorization server to log in to`)
}

// authServerMetadata is the part of RFC 8414 and OIDC discovery this client
// reads. The scopes and methods are read as refusals: they are how a server says
// it does not support what this client is about to send.
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// discoverResource reads the protected resource metadata of one server.
//
// advertised is the address a 401 named with resource_metadata, which is the only
// address the server is guaranteed to publish. The two well-known guesses follow,
// because a server may answer 401 without the header, and the bare path follows
// the suffixed one because a server mounted at /mcp may publish either.
//
// A 4xx or a 502 from a candidate means "not here" and moves on: a server
// answering 404 for an unknown path is the normal case, and treating it as an
// error would make the whole discovery fail on servers that publish nothing at
// the suffixed address. Anything else is a real failure and stops the search.
func discoverResource(ctx context.Context, hc *http.Client, serverURL *url.URL, advertised string) (resourceMetadata, error) {
	origin := serverURL.Scheme + "://" + serverURL.Host
	candidates := []string{strings.TrimSpace(advertised)}
	if path := strings.TrimSuffix(serverURL.Path, "/"); path != "" {
		candidates = append(candidates, origin+protectedResourcePath+path)
	}
	candidates = append(candidates, origin+protectedResourcePath)

	var tried []string
	for _, candidate := range compactAddresses(candidates) {
		body, status, err := getJSON(ctx, hc, candidate)
		if err != nil {
			return resourceMetadata{}, err
		}
		switch {
		case status == http.StatusOK:
			var document resourceMetadata
			if err := json.Unmarshal(body, &document); err != nil {
				return resourceMetadata{}, fmt.Errorf("protected resource metadata %s: %w", candidate, err)
			}
			return document, nil
		case status >= 400 && status < 500, status == http.StatusBadGateway:
			tried = append(tried, candidate)
		default:
			return resourceMetadata{}, fmt.Errorf("protected resource metadata: %s answered %d", candidate, status)
		}
	}
	return resourceMetadata{}, fmt.Errorf("no protected resource metadata: none of %s answered", strings.Join(tried, ", "))
}

// discoverAuthServer reads the metadata of one authorization server.
//
// The issuer check that follows the fetch is not decoration: without it a
// document served by anything on the path — a proxy, a captive portal, the wrong
// tenant — could hand this client a registration endpoint of its own choosing.
// RFC 8414 section 3.3 and the MCP specification both require the field to name
// the issuer it was fetched for, so a mismatch is a refusal rather than a
// fallback to the next address.
func discoverAuthServer(ctx context.Context, hc *http.Client, issuer string) (authServerMetadata, error) {
	parsed, err := url.Parse(issuer)
	if err != nil {
		return authServerMetadata{}, fmt.Errorf("the resource metadata names %q as its authorization server, which is not a URL: %w", issuer, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return authServerMetadata{}, fmt.Errorf("the resource metadata names %q as its authorization server, which is not an http address", issuer)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	path := strings.TrimSuffix(parsed.Path, "/")
	candidates := []string{
		origin + authorizationServerPath + path,
		origin + oidcConfigurationPath + path,
	}
	if path != "" {
		// The pre-RFC OIDC shape: the well-known segment after the path rather
		// than before it. Only an issuer with a path has this form.
		candidates = append(candidates, origin+path+oidcConfigurationPath)
	}

	var tried []string
	for _, candidate := range candidates {
		body, status, err := getJSON(ctx, hc, candidate)
		if err != nil {
			return authServerMetadata{}, err
		}
		if status != http.StatusOK {
			tried = append(tried, candidate)
			continue
		}
		var document authServerMetadata
		if err := json.Unmarshal(body, &document); err != nil {
			return authServerMetadata{}, fmt.Errorf("%s: %w", candidate, err)
		}
		if err := checkIssuer(document.Issuer, issuer, candidate); err != nil {
			return authServerMetadata{}, err
		}
		document.Issuer = strings.TrimSuffix(issuer, "/")
		if err := document.validate(); err != nil {
			return authServerMetadata{}, fmt.Errorf("%s: %w", candidate, err)
		}
		return document, nil
	}
	return authServerMetadata{}, fmt.Errorf("no %s: none of %s answered", authorizationServerDocID, strings.Join(tried, ", "))
}

// checkIssuer refuses a document that does not name the issuer it was fetched for.
func checkIssuer(found, expected, source string) error {
	if strings.TrimSuffix(found, "/") == strings.TrimSuffix(expected, "/") {
		return nil
	}
	return fmt.Errorf(`%s names issuer %q, but it was fetched through %q; a document that does not name itself is not that authorization server's`, source, found, expected)
}

// validate checks the endpoints this client is about to call.
func (m authServerMetadata) validate() error {
	if m.AuthorizationEndpoint == "" {
		return errors.New(`names no "authorization_endpoint"`)
	}
	if m.TokenEndpoint == "" {
		return errors.New(`names no "token_endpoint"`)
	}
	if err := checkEndpoint("authorization_endpoint", m.AuthorizationEndpoint); err != nil {
		return err
	}
	if err := checkEndpoint("token_endpoint", m.TokenEndpoint); err != nil {
		return err
	}
	if m.RegistrationEndpoint != "" {
		if err := checkEndpoint("registration_endpoint", m.RegistrationEndpoint); err != nil {
			return err
		}
	}
	return nil
}

// checkEndpoint refuses an endpoint that is not https, except on a loopback
// address. A downgrade here is the whole authorization: the code, the verifier
// and the tokens all travel through these two addresses.
func checkEndpoint(field, address string) error {
	parsed, err := url.Parse(address)
	if err != nil {
		return fmt.Errorf("%s %q: %w", field, address, err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s %q has no host", field, address)
	}
	switch {
	case parsed.Scheme == "https":
		return nil
	case parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()):
		// A server on this machine cannot be eavesdropped on, and refusing it
		// would make every local test server need a certificate.
		return nil
	default:
		return fmt.Errorf("%s %q is not https; an authorization server on a plain connection would send the code and the tokens in the clear", field, address)
	}
}

// isLoopbackHost reports whether host addresses this machine.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// selectResource picks the resource identifier to send with the authorize and
// token requests (RFC 8707).
//
// The token is only good for the resource it was issued for, so the identifier has
// to be this server's: same origin, and a path that neither widens past the
// server nor points somewhere else on the same host.
func selectResource(prm resourceMetadata, serverURL *url.URL) (string, error) {
	resource := strings.TrimSpace(prm.Resource)
	if resource == "" {
		// The field is required by RFC 9728, but a document without it still
		// says where the metadata lives. The server's own URL is the audience
		// this client is asking for, and asking for anything else would be
		// inventing one.
		resource = serverURL.String()
	}
	parsed, err := url.Parse(resource)
	if err != nil {
		return "", fmt.Errorf("the protected resource metadata names %q as its resource, which is not a URL: %w", resource, err)
	}
	if parsed.Scheme != serverURL.Scheme || parsed.Host != serverURL.Host {
		return "", fmt.Errorf("the protected resource metadata names %q as its resource, which is not on %s; a token for another origin is a token for another server", resource, serverURL.Host)
	}
	if !prefixAtSegment(parsed.Path, serverURL.Path) && !prefixAtSegment(serverURL.Path, parsed.Path) {
		return "", fmt.Errorf("the protected resource metadata names %q as its resource, which is neither the server's URL nor a prefix of it", resource)
	}
	return resource, nil
}

// prefixAtSegment reports whether prefix is a prefix of path that ends on a
// segment boundary, so that "/mcp" prefixes "/mcp/tools" but not "/mcpx".
func prefixAtSegment(prefix, path string) bool {
	if prefix == "" || prefix == "/" || prefix == path {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return strings.HasPrefix(path[len(prefix):], "/")
}

// getJSON fetches one metadata document.
//
// The status is returned with the body rather than folded into an error because
// the caller decides what "not here" means: 404 is expected for a well-known
// address a server does not publish, and a network failure is not.
func getJSON(ctx context.Context, hc *http.Client, address string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", address, err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := hc.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", address, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// Drain a little so the connection can be reused. The body is not read
		// into the error: on a 404 it belongs to a gateway, not to the server.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 512))
		return nil, response.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthDocumentBytes))
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("%s: %w", address, err)
	}
	if len(body) == 0 {
		return nil, response.StatusCode, fmt.Errorf("%s: the answer is empty", address)
	}
	return body, response.StatusCode, nil
}

// compactAddresses drops the empty entries and the repeats, keeping the order.
// A server with no path produces the same candidate twice.
func compactAddresses(addresses []string) []string {
	kept := addresses[:0]
	seen := make(map[string]bool, len(addresses))
	for _, address := range addresses {
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		kept = append(kept, address)
	}
	return kept
}

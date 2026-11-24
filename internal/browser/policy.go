package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const maxURLLength = 8192

var privateHostnames = map[string]bool{
	"localhost":                true,
	"localhost.localdomain":    true,
	"metadata.google.internal": true,
	"metadata.google":          true,
}

var sensitiveQueryPattern = regexp.MustCompile(`(?i)(?:^|[-_.])(?:access|api|auth|authorization|code|credential|key|nonce|otp|pass|password|secret|session|signature|sig|state|token)(?:$|[-_.])`)

// URLPolicyOptions governs which URLs a navigation attempt may reach.
type URLPolicyOptions struct {
	UserInitiated       bool
	AllowPrivateNetwork bool
}

// ResolveAddress turns user input (a full URL, a bare domain/localhost, or a
// search phrase) into a navigable URL. When allowSearch is false, only explicit
// http(s) URLs or about:blank are accepted.
func ResolveAddress(raw string, allowSearch bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "about:blank", nil
	}
	if len(trimmed) > maxURLLength || strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", errors.New("browser: invalid address")
	}
	if strings.EqualFold(trimmed, "about:blank") {
		return "about:blank", nil
	}
	if strings.Contains(trimmed, "://") {
		return trimmed, nil
	}
	if !allowSearch {
		return "", errors.New("browser: URL must be an absolute http(s) URL")
	}
	if !strings.ContainsAny(trimmed, " \t") && looksLikeHostOrLocal(trimmed) {
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "localhost") || strings.HasPrefix(lower, "127.") || strings.HasPrefix(lower, "[::1]") {
			return "http://" + trimmed, nil
		}
		return "https://" + trimmed, nil
	}
	return "https://www.google.com/search?q=" + url.QueryEscape(trimmed), nil
}

func looksLikeHostOrLocal(s string) bool {
	host := s
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	if strings.HasPrefix(strings.ToLower(host), "localhost") {
		return true
	}
	if strings.Contains(host, ":") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	return strings.Contains(host, ".")
}

// CheckNavigationURL validates rawURL and enforces Dexon-style SSRF rules:
// embedded credentials and non-http(s) schemes are always blocked, and private
// or loopback targets are blocked unless user-initiated or explicitly allowed.
func CheckNavigationURL(ctx context.Context, rawURL string, opts URLPolicyOptions) (string, error) {
	if rawURL == "" || len(rawURL) > maxURLLength || strings.ContainsAny(rawURL, "\x00\r\n") {
		return "", errors.New("browser: URL is invalid")
	}
	if rawURL == "about:blank" {
		return rawURL, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !parsed.IsAbs() {
		return "", errors.New("browser: URL must be absolute")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("browser: unsupported protocol %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return "", errors.New("browser: URLs containing credentials are blocked")
	}
	host := normalizeHost(parsed.Hostname())
	if host == "" {
		return "", errors.New("browser: URL has no hostname")
	}
	if !opts.UserInitiated && !opts.AllowPrivateNetwork {
		if isPrivateHostname(host) || isPrivateIPString(host) {
			return "", fmt.Errorf("browser: agent navigation to private network host %q is blocked", host)
		}
		if net.ParseIP(host) == nil {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err == nil {
				for _, addr := range addrs {
					if isPrivateIP(addr.IP) {
						return "", fmt.Errorf("browser: host %q resolves to private address %s", host, addr.IP)
					}
				}
			}
		}
	}
	return parsed.String(), nil
}

// RedactURL removes userinfo and sensitive query values before showing URLs to
// the model or recording them in summaries.
func RedactURL(rawURL string) string {
	if rawURL == "" || rawURL == "about:blank" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.User = nil
	if parsed.RawQuery != "" {
		q := parsed.Query()
		changed := false
		for key := range q {
			if sensitiveQueryPattern.MatchString(key) {
				q.Set(key, "[REDACTED]")
				changed = true
			}
		}
		if changed {
			parsed.RawQuery = q.Encode()
		}
	}
	return parsed.String()
}

func normalizeHost(host string) string {
	h := strings.TrimSpace(strings.ToLower(host))
	h = strings.TrimSuffix(h, ".")
	return strings.Trim(h, "[]")
}

func isPrivateHostname(host string) bool {
	return privateHostnames[host] || strings.HasSuffix(host, ".localhost")
}

func isPrivateIPString(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return isPrivateIP(ip)
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if v4.IsLoopback() || v4.IsPrivate() || v4.IsLinkLocalUnicast() || v4.IsLinkLocalMulticast() || v4.IsUnspecified() {
			return true
		}
		// RFC 6598 CGNAT 100.64.0.0/10
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

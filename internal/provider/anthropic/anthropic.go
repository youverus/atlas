// Package anthropic implements the Anthropic messages wire API, the protocol
// behind api.anthropic.com and the gateways that expose the same shape.
//
// Like the openai adapter it is stateless apart from its Compat values, so one
// Provider can serve concurrent streams (a side conversation runs next to the
// main one). Failure policy is identical: everything after the request is built
// becomes a terminal assistant message, and Stream only returns an error for a
// request that cannot be represented on the wire.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// APIName is the routing key a model declaration uses to reach this adapter.
const APIName = "anthropic-messages"

const (
	// defaultBaseURL includes the version segment because every deployment of
	// this API serves it under /v1, including self-hosted gateways.
	defaultBaseURL = "https://api.anthropic.com/v1"
	// defaultVersion is the oldest messages API version still in use. The
	// header is required; omitting it is rejected.
	defaultVersion = "2023-06-01"
	// maxErrorBody bounds how much of an error response is kept.
	maxErrorBody = 8 << 10
)

// Compat holds the per-deployment values this adapter needs.
type Compat struct {
	// BaseURL is the API root including the version segment, for example
	// https://api.anthropic.com/v1. A model declaration carrying its own
	// BaseURL wins over this value.
	BaseURL string

	// Version is the anthropic-version header. Empty uses defaultVersion.
	Version string

	// ThinkingDialect selects the thinking fields. Empty sends the budget the
	// messages API documents; "deepseek" sends the toggle and the effort dial
	// DeepSeek put on its Anthropic-compatible endpoint instead.
	ThinkingDialect string

	// AdaptiveThinking sends the newer thinking form: the model decides how
	// much to think, and the level becomes an effort instead of a token budget.
	// Opt in, because a model that predates it rejects the type outright.
	AdaptiveThinking bool

	// StrictTools lets a tool that declares wire.Tool.Strict travel with
	// strict:true, so the API validates arguments against the schema. Opt in:
	// the endpoint has to support constrained decoding, and a rejection costs
	// the whole request.
	StrictTools bool

	// ToolChoice asks the model to call a tool ("required"), to choose
	// ("auto"), or to answer in prose ("none"). Empty sends nothing and leaves
	// the vendor default, which is "auto".
	ToolChoice string
}

// Provider is the messages adapter.
type Provider struct {
	Compat Compat

	// Client overrides the HTTP client. Nil uses http.DefaultClient.
	Client *http.Client
}

// New returns a provider with the given settings.
func New(compat Compat) *Provider { return &Provider{Compat: compat} }

// API reports the routing key for this adapter.
func (p *Provider) API() string { return APIName }

// Stream sends the request and returns a stream of assistant events.
func (p *Provider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	payload, err := p.buildPayload(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	// The declaration decides what this endpoint is told about its client. An
	// empty value keeps net/http's own header, which is the only answer that
	// says nothing about this binary.
	if ua := req.Model.UserAgent; ua != "" {
		httpReq.Header.Set("User-Agent", ua)
	}
	httpReq.Header.Set("anthropic-version", p.version())
	// Anthropic uses x-api-key rather than a bearer token. An empty key is not
	// rejected here: a proxy in front of the API may authenticate on its own.
	if key := req.Options.APIKey; key != "" {
		httpReq.Header.Set("x-api-key", key)
	}
	for name, value := range req.Options.Headers {
		httpReq.Header.Set(name, value)
	}

	resp, err := p.client().Do(httpReq)
	if err != nil {
		return provider.FailedStream(req.Model, failureReason(ctx), err.Error()), nil
	}
	if resp.StatusCode != http.StatusOK {
		message := responseError(resp)
		resp.Body.Close()
		return provider.FailedStream(req.Model, wire.StopError, message), nil
	}
	return newStream(ctx, req.Model, resp.Body), nil
}

func (p *Provider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

func (p *Provider) endpoint(model wire.Model) string {
	return p.baseURL(model) + "/messages"
}

func (p *Provider) baseURL(model wire.Model) string {
	var raw string
	switch {
	case model.BaseURL != "":
		raw = model.BaseURL
	case p.Compat.BaseURL != "":
		raw = p.Compat.BaseURL
	default:
		return defaultBaseURL
	}
	return NormalizeBaseURL(raw)
}

// NormalizeBaseURL normalizes an anthropic-messages base URL so compatible
// endpoints work whether configured as an Anthropic SDK root (for example
// https://api.anthropic.com, https://open.bigmodel.cn/api/anthropic,
// https://api.minimaxi.com/anthropic, https://dashscope.aliyuncs.com/apps/anthropic,
// https://openrouter.ai/api), a full /messages endpoint URL, or a versioned
// root ending in /v1.
//
// Bare loopback origins (http://127.0.0.1:<port>) are preserved as-is so
// in-process test servers listening at /messages continue to work; user
// configuration callers should use NormalizeConfiguredBaseURL.
func NormalizeBaseURL(raw string) string {
	return normalizeBaseURL(raw, false)
}

// NormalizeConfiguredBaseURL normalizes a user-configured anthropic-messages
// base URL, including bare localhost/loopback proxies (for example
// http://localhost:4000 -> http://localhost:4000/v1).
func NormalizeConfiguredBaseURL(raw string) string {
	return normalizeBaseURL(raw, true)
}

func normalizeBaseURL(raw string, bareLoopbackV1 bool) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return trimmed
	}

	host := strings.ToLower(parsed.Hostname())
	path := strings.TrimRight(parsed.Path, "/")
	lowerPath := strings.ToLower(path)

	// Users frequently paste the full .../messages endpoint URL into baseUrl.
	if lowerPath == "/messages" || strings.HasSuffix(lowerPath, "/messages") {
		path = strings.TrimRight(path[:len(path)-len("/messages")], "/")
		lowerPath = strings.ToLower(path)
	}

	// DeepSeek serves Anthropic Messages under /anthropic/v1 rather than /v1.
	if host == "api.deepseek.com" {
		switch lowerPath {
		case "", "/v1", "/anthropic", "/anthropic/v1":
			parsed.Path = "/anthropic/v1"
			parsed.RawPath = ""
			return parsed.String()
		}
	}

	if lowerPath == "" {
		if bareLoopbackV1 || !isLoopbackHost(host) {
			parsed.Path = "/v1"
		} else {
			parsed.Path = ""
		}
		parsed.RawPath = ""
		return strings.TrimRight(parsed.String(), "/")
	}

	lastSeg := lowerPath
	if idx := strings.LastIndexByte(lowerPath, '/'); idx >= 0 {
		lastSeg = lowerPath[idx+1:]
	}
	if isVersionSegment(lastSeg) {
		parsed.Path = path
		parsed.RawPath = ""
		return parsed.String()
	}

	// Common SDK-style mount roots for Anthropic-compatible APIs omit /v1.
	switch lastSeg {
	case "anthropic", "coding", "api":
		parsed.Path = path + "/v1"
		parsed.RawPath = ""
		return parsed.String()
	default:
		parsed.Path = path
		parsed.RawPath = ""
		return parsed.String()
	}
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func isVersionSegment(seg string) bool {
	return len(seg) >= 2 && seg[0] == 'v' && seg[1] >= '0' && seg[1] <= '9'
}

func (p *Provider) version() string {
	if p.Compat.Version != "" {
		return p.Compat.Version
	}
	return defaultVersion
}

// failureReason separates a cancelled call from a genuine transport failure.
func failureReason(ctx context.Context) wire.StopReason {
	if ctx.Err() != nil {
		return wire.StopAborted
	}
	return wire.StopError
}

// responseError turns a non-200 response into a message the caller can log.
// The API answers with {"type":"error","error":{"type":...,"message":...}};
// gateways in front of it sometimes answer with a bare message or plain text,
// so all three shapes are handled.
func responseError(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	text := strings.TrimSpace(string(body))

	var envelope struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Error != nil && envelope.Error.Message != "" {
			return fmt.Sprintf("%s: %s", resp.Status, envelope.Error.Message)
		}
		if envelope.Message != "" {
			return fmt.Sprintf("%s: %s", resp.Status, envelope.Message)
		}
	}
	if text == "" {
		return resp.Status
	}
	if len(text) > maxErrorBody {
		text = text[:maxErrorBody]
	}
	return fmt.Sprintf("%s: %s", resp.Status, text)
}

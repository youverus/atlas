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
	switch {
	case model.BaseURL != "":
		return strings.TrimRight(model.BaseURL, "/")
	case p.Compat.BaseURL != "":
		return strings.TrimRight(p.Compat.BaseURL, "/")
	default:
		return defaultBaseURL
	}
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

// Package google implements the Gemini generative language wire API, the
// protocol behind generativelanguage.googleapis.com and the gateways that
// expose the same shape.
//
// Like the other adapters it is stateless apart from its Compat values, so one
// Provider can serve concurrent streams. Failure policy is identical:
// everything after the request is built becomes a terminal assistant message,
// and Stream only returns an error for a request that cannot be represented on
// the wire.
package google

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
const APIName = "google-generative-ai"

const (
	// defaultBaseURL includes the version segment, which is how this API is
	// served: the version is part of the path, not a header.
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	// maxErrorBody bounds how much of an error response is kept.
	maxErrorBody = 8 << 10
)

// The thinking dialects this API serves. A model family decides which one it
// speaks, so the adapter sniffs the model id and a compat value overrides the
// guess when a gateway renamed the model.
const (
	// DialectLevel sends thinkingLevel, the coarse gear newer Gemini models
	// take.
	DialectLevel = "level"
	// DialectBudget sends thinkingBudget, the token allowance older models
	// take.
	DialectBudget = "budget"
)

// Compat holds the per-deployment values this adapter needs.
type Compat struct {
	// BaseURL is the API root including the version segment, for example
	// https://generativelanguage.googleapis.com/v1beta. A model declaration
	// carrying its own BaseURL wins over this value.
	BaseURL string

	// ThinkingDialect is "level" or "budget". Empty sniffs the model id, which
	// is what the vendor's own catalogue implies.
	ThinkingDialect string

	// ToolChoice asks the model to call a function ("required"), to choose
	// ("auto"), or to answer in prose ("none"). Empty sends no mode at all and
	// leaves the vendor default.
	ToolChoice string
}

// Provider is the generative language adapter.
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
		return nil, fmt.Errorf("google: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("google: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	// The declaration decides what this endpoint is told about its client. An
	// empty value keeps net/http's own header, which is the only answer that
	// says nothing about this binary.
	if ua := req.Model.UserAgent; ua != "" {
		httpReq.Header.Set("User-Agent", ua)
	}
	// This API takes the key in its own header rather than a bearer token. An
	// empty key is not rejected here: a proxy may authenticate on its own.
	if key := req.Options.APIKey; key != "" {
		httpReq.Header.Set("x-goog-api-key", key)
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

// endpoint builds the streaming path. The model is a path segment, so it is
// escaped: a gateway may serve names containing a colon or a slash.
func (p *Provider) endpoint(model wire.Model) string {
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", p.baseURL(model), url.PathEscape(model.ID))
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

// failureReason separates a cancelled call from a genuine transport failure.
func failureReason(ctx context.Context) wire.StopReason {
	if ctx.Err() != nil {
		return wire.StopAborted
	}
	return wire.StopError
}

// responseError turns a non-200 response into a message the caller can log.
// This API answers with {"error":{"code":...,"message":...,"status":...}};
// gateways in front of it sometimes answer with a bare message or plain text,
// so all three shapes are handled.
func responseError(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	text := strings.TrimSpace(string(body))

	var envelope struct {
		Error *struct {
			Message string `json:"message"`
			Status  string `json:"status"`
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

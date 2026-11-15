// Package responses implements the OpenAI Responses wire API: the protocol
// behind api.openai.com/v1/responses and the servers that copy its shape.
//
// The adapter is deliberately stateless. The request always says store:false and
// never carries previous_response_id, so a conversation lives in this process's
// transcript and nowhere else: the same request can be replayed, forked, or
// compacted without asking the vendor what it remembers. What that costs is a
// reasoning item that cannot be regenerated — the server hands it back encrypted
// and this adapter stores it verbatim in the thinking block's signature so a
// later turn can hand it back.
//
// Like the other adapters, everything that can happen after the request is built
// becomes a terminal assistant message, never a Go error, and one Provider
// serves concurrent streams.
package responses

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
const APIName = "openai-responses"

const (
	defaultBaseURL = "https://api.openai.com/v1"
	// maxErrorBody bounds how much of an error response is kept for the message.
	maxErrorBody = 8 << 10
)

// Compat holds the per-deployment values this adapter needs.
type Compat struct {
	// BaseURL is the API root, for example https://api.openai.com/v1. A model
	// declaration carrying its own BaseURL wins over this value.
	BaseURL string

	// ToolChoice asks the model to call a tool ("required"), to choose ("auto"),
	// or to answer in prose ("none"). Empty sends nothing and leaves the vendor
	// default, which is "auto".
	ToolChoice string

	// ServiceTier selects the serving queue: "auto", "default", "flex" or
	// "priority". Empty sends nothing.
	ServiceTier string

	// Verbosity is how much prose the answer carries: "low", "medium" or
	// "high". Empty sends nothing.
	Verbosity string

	// Truncation is "auto" or "disabled": whether the server may drop the head
	// of a conversation that outgrew the window. Empty sends nothing, which
	// leaves the API's own default.
	Truncation string
}

// Provider is the Responses adapter.
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
		return nil, fmt.Errorf("responses: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("responses: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if key := req.Options.APIKey; key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
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
	return p.baseURL(model) + "/responses"
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
// The API answers with {"error":{"message":...}}; gateways in front of it
// sometimes answer with {"message":...} or plain text, so all three shapes are
// handled. It is a copy of the chat completions adapter's version rather than a
// shared one: adapters must not import each other, and the two error envelopes
// are free to drift.
func responseError(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	text := strings.TrimSpace(string(body))

	var envelope struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
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

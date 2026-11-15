// Package openai implements the chat completions wire API: the protocol spoken
// by OpenAI itself and by most compatible vendors (DeepSeek, Qwen, Kimi, GLM,
// OpenRouter, vLLM, llama.cpp ...).
//
// The adapter is stateless apart from its Compat flags: it converts wire
// messages into a chat request, opens a Server-Sent Events response, and feeds
// decoded chunks into provider.Frame. One Provider can therefore serve
// concurrent streams.
//
// Failure policy: everything that can happen after the request is built is
// reported as a terminal assistant message, never as a Go error. Stream only
// returns an error for programming mistakes, such as a message that cannot be
// represented on the wire.
package openai

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
const APIName = "openai-completions"

const (
	defaultBaseURL = "https://api.openai.com/v1"
	// maxErrorBody bounds how much of an error response is kept for the message.
	maxErrorBody = 8 << 10
)

// Compat holds the per-vendor switches this adapter needs. The zero value is
// tuned for api.openai.com; every field exists because some compatible server
// rejects or requires the opposite.
type Compat struct {
	// BaseURL is the API root, for example https://api.deepseek.com/v1. A model
	// declaration carrying its own BaseURL wins over this value.
	BaseURL string

	// MaxTokensField selects the request field for the output limit:
	// "max_tokens" or "max_completion_tokens". Empty picks by host, because
	// api.openai.com rejects max_tokens on reasoning models while most compatible
	// servers never learned max_completion_tokens.
	MaxTokensField string

	// RequireReasoningContent always writes reasoning_content on assistant
	// messages, even when the model produced none. DeepSeek requires the field on
	// replay once a conversation contains reasoning turns.
	RequireReasoningContent bool

	// DisableStreamOptions stops sending stream_options.include_usage. Some
	// gateways reject the field instead of ignoring it.
	DisableStreamOptions bool

	// TolerateMissingFinishReason accepts a stream that closes without a
	// finish_reason and infers stop or toolUse from the content. Off by default:
	// a truncated stream must fail loudly instead of looking complete.
	TolerateMissingFinishReason bool

	// RequireToolResultName writes name on tool messages. A few vendors reject
	// tool results without it.
	RequireToolResultName bool

	// RequireAssistantAfterToolResult inserts a synthetic assistant turn after
	// tool results, for providers that refuse a user message (or another
	// assistant turn with a string content) directly after them.
	RequireAssistantAfterToolResult bool

	// ThinkingDialect selects the thinking fields. Empty sends the standard
	// effort dial, which is what api.openai.com and most reasoning-model
	// deployments expect from a client that has a gear to report; "deepseek"
	// sends DeepSeek's thinking object and its own effort field instead.
	ThinkingDialect string

	// DisableReasoningEffort stops the standard dialect from writing
	// reasoning_effort. Some gateways reject the field instead of ignoring it,
	// and the level is then dropped rather than the request.
	DisableReasoningEffort bool

	// ToolChoice pins tool calling to "auto", "none" or "required" on every
	// request. Empty leaves the field off, which is the endpoint default.
	ToolChoice string

	// SendStore writes "store": false, telling the endpoint not to keep the
	// response. atlas replays its own transcript, so a server-side copy would
	// be state no later request references. Opt-in because a proxy in front of
	// the API may reject the field.
	SendStore bool
}

// Provider is the chat completions adapter.
type Provider struct {
	Compat Compat

	// Client overrides the HTTP client. Nil uses http.DefaultClient.
	Client *http.Client
}

// New returns a provider with the given compatibility flags.
func New(compat Compat) *Provider { return &Provider{Compat: compat} }

// API reports the routing key for this adapter.
func (p *Provider) API() string { return APIName }

// Stream sends the request and returns a stream of assistant events.
func (p *Provider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	messages, err := p.convertMessages(req)
	if err != nil {
		return nil, err
	}
	payload := p.buildPayload(req, messages)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
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
	return newStream(ctx, req.Model, resp.Body, p.Compat.TolerateMissingFinishReason), nil
}

func (p *Provider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

func (p *Provider) endpoint(model wire.Model) string {
	return p.baseURL(model) + "/chat/completions"
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

func (p *Provider) maxTokensField(model wire.Model) string {
	if p.Compat.MaxTokensField != "" {
		return p.Compat.MaxTokensField
	}
	// api.openai.com rejects max_tokens on reasoning models; compatible servers
	// usually reject max_completion_tokens. The host decides, and a BaseURL that
	// does not parse falls back to the field every server understands.
	if parsed, err := url.Parse(p.baseURL(model)); err == nil && parsed.Hostname() == "api.openai.com" {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

// failureReason separates a cancelled call from a genuine transport failure.
func failureReason(ctx context.Context) wire.StopReason {
	if ctx.Err() != nil {
		return wire.StopAborted
	}
	return wire.StopError
}

// responseError turns a non-200 response into a message the caller can log.
// OpenAI and compatible servers use {"error": {"message": ...}}; a few gateways
// answer with {"message": ...} or plain text, so both are handled.
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

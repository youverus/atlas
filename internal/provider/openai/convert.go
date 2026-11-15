package openai

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// payload is the chat completions request body. MaxTokens and
// MaxCompletionTokens are mutually exclusive: exactly one carries the limit,
// chosen by Compat.MaxTokensField.
type payload struct {
	Model               string         `json:"model"`
	Messages            []chatMessage  `json:"messages"`
	Stream              bool           `json:"stream"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	MaxTokens           *int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
	Tools               []chatTool     `json:"tools,omitempty"`
	ToolChoice          string         `json:"tool_choice,omitempty"`

	// Store is a pointer so "false" travels while "not asked" stays off the
	// wire: the two mean different things to an endpoint that keeps responses
	// by default.
	Store *bool `json:"store,omitempty"`

	// Thinking and ReasoningEffort are the thinking dials. Both stay absent
	// under the empty dialect when the model declares no reasoning, so the
	// vendor default applies untouched.
	Thinking        *thinkingParam `json:"thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

// thinkingParam is DeepSeek's thinking object: a switch, not a token budget.
type thinkingParam struct {
	Type string `json:"type"`
}

// The two values DeepSeek documents for thinking.type.
const (
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"
)

// dialectDeepSeek is the Compat.ThinkingDialect value that switches the
// thinking fields to the ones DeepSeek documents.
const dialectDeepSeek = "deepseek"

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage is the union of every role the API accepts. Content is raw so an
// assistant turn can carry null while the other roles carry a string or parts.
type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`

	// Reasoning replay. Vendors disagree on the field name, so the decoder
	// records the one it saw in the thinking block signature and that name is
	// reused here.
	ReasoningContent *string `json:"reasoning_content,omitempty"`
	Reasoning        *string `json:"reasoning,omitempty"`
	ReasoningText    *string `json:"reasoning_text,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolSpec `json:"function"`
}

type chatToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

func (p *Provider) buildPayload(req provider.Request, messages []chatMessage) payload {
	out := payload{
		Model:    req.Model.ID,
		Messages: messages,
		Stream:   true,
	}
	if !p.Compat.DisableStreamOptions {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if p.Compat.SendStore {
		out.Store = &falseValue
	}
	out.ToolChoice = p.Compat.ToolChoice
	// One effort field, two owners: the DeepSeek dialect pairs it with a
	// thinking object, the standard dialect writes it alone. Both arrive here
	// as a resolved pair so neither path can forget the second half.
	out.Thinking, out.ReasoningEffort = p.thinkingFields(req.Model, req.Options.ThinkingLevel)
	thinking := out.Thinking
	// Temperature is left out while thinking runs: DeepSeek ignores it there,
	// and sending a value that is silently discarded would suggest the two
	// settings compose. This is the rule the anthropic adapter already applies
	// to a thinking budget. The standard effort dial has no such conflict, so
	// the value travels with it.
	if req.Options.Temperature != nil && (thinking == nil || thinking.Type != thinkingEnabled) {
		out.Temperature = req.Options.Temperature
	}
	if req.Options.MaxTokens != nil {
		if p.maxTokensField(req.Model) == "max_tokens" {
			out.MaxTokens = req.Options.MaxTokens
		} else {
			out.MaxCompletionTokens = req.Options.MaxTokens
		}
	}
	if tools := wire.CurrentTools(req.Messages); len(tools) > 0 {
		out.Tools = convertTools(tools)
	}
	return out
}

// falseValue carries "store": false without allocating per request.
var falseValue = false

// thinkingFields resolves the thinking parameters for a level. No thinking
// object means the level asked for nothing at all: an empty level leaves the
// vendor default alone, and DeepSeek thinks at high effort unless told
// otherwise.
func (p *Provider) thinkingFields(model wire.Model, level string) (*thinkingParam, string) {
	if p.Compat.ThinkingDialect != dialectDeepSeek {
		return nil, p.effortField(model, level)
	}
	switch level {
	case "":
		return nil, ""
	case "off":
		return &thinkingParam{Type: thinkingDisabled}, ""
	default:
		return &thinkingParam{Type: thinkingEnabled}, deepSeekEffort(level)
	}
}

// effortField resolves the standard reasoning_effort dial, the one
// api.openai.com and the o-series deployments document.
//
// The model has to declare reasoning, unlike the DeepSeek path: there the
// vendor thinks unless told otherwise, so "off" is worth saying. Here an
// unknown endpoint that merely borrowed the id must not start receiving a
// field it never agreed to read. "off" has no value in this vocabulary — the
// API has no "none" — so it is left out and the endpoint's own default stands.
func (p *Provider) effortField(model wire.Model, level string) string {
	if p.Compat.DisableReasoningEffort || !model.Reasoning {
		return ""
	}
	switch level {
	case "", "off":
		return ""
	default:
		return openAIEffort(level)
	}
}

// openAIEffort maps atlas's six gears onto the effort vocabulary the chat
// completions API documents. It has no gear above high, so xhigh, max and ultra
// clamp; an unrecognised level takes the vendor default rather than being
// dropped.
func openAIEffort(level string) string {
	switch level {
	case "low", "medium", "high":
		return level
	default:
		return "high"
	}
}

// deepSeekEffort maps atlas's six gears onto the effort vocabulary DeepSeek
// documents: low survives, medium/high/xhigh share high, and max and ultra share
// max. An unrecognised level takes high, the vendor default, rather than being
// dropped.
func deepSeekEffort(level string) string {
	switch level {
	case "low":
		return "low"
	case "max", "ultra":
		return "max"
	default:
		return "high"
	}
}

func convertTools(tools []wire.Tool) []chatTool {
	out := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			// A missing schema is rejected by strict servers; an empty object is
			// accepted everywhere.
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolSpec{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return out
}

// Placeholders that keep a tool call paired with a non-empty output, since some
// servers reject an empty tool message.
const (
	emptyToolOutput = "(no tool output)"
	imageToolOutput = "(see attached image)"

	// toolResultImageNote introduces the user message that carries images found
	// in tool results, and processedToolResults bridges the same gap as an
	// assistant turn when a provider forbids tool -> user adjacency.
	toolResultImageNote      = "Attached image(s) from tool result:"
	processedToolResultsText = "I have processed the tool results."
)

func (p *Provider) convertMessages(req provider.Request) ([]chatMessage, error) {
	out := make([]chatMessage, 0, len(req.Messages))
	// lastRole tracks which role the last emitted message had, so the
	// RequireAssistantAfterToolResult bridge knows whether it is needed.
	var lastRole wire.Role
	for i := 0; i < len(req.Messages); i++ {
		message := req.Messages[i]
		if p.Compat.RequireAssistantAfterToolResult && lastRole == wire.RoleToolResult && message.Role == wire.RoleUser {
			out = append(out, chatMessage{Role: "assistant", Content: textContent(processedToolResultsText)})
			lastRole = wire.RoleAssistant
		}
		before := len(out)
		switch message.Role {
		case wire.RoleSystem:
			// The system prompt and the tool declarations are separate fields on
			// the wire; tool declarations travel in payload.Tools.
			if text := message.Text(); text != "" {
				out = append(out, chatMessage{Role: "system", Content: textContent(text)})
			}
		case wire.RoleUser:
			converted, err := convertUser(message)
			if err != nil {
				return nil, err
			}
			if converted != nil {
				out = append(out, *converted)
			}
		case wire.RoleAssistant:
			if converted := p.convertAssistant(message); converted != nil {
				out = append(out, *converted)
			}
		case wire.RoleToolResult:
			// Consecutive tool results become one run: each result is a tool
			// message, and the images among them are collected into a single
			// following user message, because the tool role carries text only.
			next, hasImages, err := p.appendToolResults(&out, req.Model, req.Messages, i)
			if err != nil {
				return nil, err
			}
			i = next - 1
			if hasImages {
				// pi treats the appended user message as the last role, so the
				// bridge above does not fire again on the next user turn.
				lastRole = wire.RoleUser
			} else {
				lastRole = wire.RoleToolResult
			}
			continue
		default:
			return nil, fmt.Errorf("openai: unsupported message role %q", message.Role)
		}
		if len(out) > before {
			lastRole = message.Role
		}
	}
	return out, nil
}

func convertUser(message wire.Message) (*chatMessage, error) {
	if message.Content.IsPlain() {
		return &chatMessage{Role: "user", Content: textContent(message.Content.PlainText())}, nil
	}
	parts := make([]chatPart, 0, message.Content.Len())
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if block.Text == "" {
				continue
			}
			parts = append(parts, chatPart{Type: "text", Text: block.Text})
		case wire.ContentImage:
			if block.Data == "" {
				continue
			}
			if block.MIMEType == "" {
				return nil, fmt.Errorf("openai: image block without mimeType")
			}
			parts = append(parts, chatPart{
				Type:     "image_url",
				ImageURL: &imageURL{URL: "data:" + block.MIMEType + ";base64," + block.Data},
			})
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	content, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("openai: encode user content: %w", err)
	}
	return &chatMessage{Role: "user", Content: content}, nil
}

func (p *Provider) convertAssistant(message wire.Message) *chatMessage {
	out := chatMessage{Role: "assistant"}
	var text strings.Builder
	var reasoning strings.Builder
	reasoningField := ""

	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			text.WriteString(block.Text)
		case wire.ContentThinking:
			if block.Thinking == "" {
				continue
			}
			reasoning.WriteString(block.Thinking)
			if reasoningField == "" && isReasoningField(block.ThinkingSignature) {
				reasoningField = block.ThinkingSignature
			}
		case wire.ContentToolCall:
			arguments := "{}"
			if len(block.Arguments) > 0 {
				arguments = string(block.Arguments)
			}
			out.ToolCalls = append(out.ToolCalls, chatToolCall{
				ID:   block.ID,
				Type: "function",
				Function: chatToolFunction{
					Name:      block.Name,
					Arguments: arguments,
				},
			})
		}
	}

	// The API accepts a plain string, not a part array, for assistant content.
	if text.Len() > 0 {
		out.Content = textContent(text.String())
	}
	if reasoning.Len() > 0 && reasoningField != "" {
		value := reasoning.String()
		switch reasoningField {
		case "reasoning_content":
			out.ReasoningContent = &value
		case "reasoning":
			out.Reasoning = &value
		case "reasoning_text":
			out.ReasoningText = &value
		}
	}
	if p.Compat.RequireReasoningContent && out.ReasoningContent == nil {
		empty := ""
		out.ReasoningContent = &empty
	}

	// Providers reject an assistant message with neither content nor calls; this
	// is what an aborted turn leaves behind.
	if out.Content == nil && len(out.ToolCalls) == 0 {
		return nil
	}
	if p.Compat.RequireAssistantAfterToolResult && out.Content == nil {
		// The same providers that need the bridge also reject null assistant
		// content, so a tool-call-only turn replays with an empty string.
		out.Content = textContent("")
	}
	return &out
}

// appendToolResults converts the run of consecutive tool results starting at
// start, appending one tool message per result. Images the model accepts are
// gathered into a single user message after the run, which is the only way the
// chat completions wire can carry them. It returns the index just past the run
// and whether that trailing user message was appended.
func (p *Provider) appendToolResults(out *[]chatMessage, model wire.Model, messages []wire.Message, start int) (int, bool, error) {
	var images []chatPart
	next := start
	for ; next < len(messages) && messages[next].Role == wire.RoleToolResult; next++ {
		message := messages[next]
		if message.ToolCallID == "" {
			return 0, false, fmt.Errorf("openai: tool result %q has no toolCallId", message.ToolName)
		}
		hasImage := false
		for _, block := range message.Content.Items() {
			if block.Type != wire.ContentImage || block.Data == "" {
				continue
			}
			hasImage = true
			if !acceptsImages(model) {
				continue
			}
			if block.MIMEType == "" {
				return 0, false, fmt.Errorf("openai: image block without mimeType")
			}
			images = append(images, chatPart{
				Type:     "image_url",
				ImageURL: &imageURL{URL: "data:" + block.MIMEType + ";base64," + block.Data},
			})
		}

		// The tool message itself carries the text only, so an image-only
		// result gets a stand-in sentence instead of an empty string.
		text := message.Content.Text()
		if text == "" {
			if hasImage {
				text = imageToolOutput
			} else {
				text = emptyToolOutput
			}
		}
		converted := chatMessage{Role: "tool", Content: textContent(text), ToolCallID: message.ToolCallID}
		if p.Compat.RequireToolResultName && message.ToolName != "" {
			converted.Name = message.ToolName
		}
		*out = append(*out, converted)
	}

	if len(images) == 0 {
		return next, false, nil
	}
	if p.Compat.RequireAssistantAfterToolResult {
		*out = append(*out, chatMessage{Role: "assistant", Content: textContent(processedToolResultsText)})
	}
	parts := make([]chatPart, 0, len(images)+1)
	parts = append(parts, chatPart{Type: "text", Text: toolResultImageNote})
	parts = append(parts, images...)
	content, err := json.Marshal(parts)
	if err != nil {
		return 0, false, fmt.Errorf("openai: encode tool result images: %w", err)
	}
	*out = append(*out, chatMessage{Role: "user", Content: content})
	return next, true, nil
}

// acceptsImages reports whether the model declared image input. An empty
// declaration means "unknown" and is treated as accepting images: dropping them
// would hide the loss from the model, while a server that rejects them fails
// loudly. A declaration without "image" is honoured as text-only.
func acceptsImages(model wire.Model) bool {
	if len(model.Input) == 0 {
		return true
	}
	return slices.Contains(model.Input, "image")
}

func isReasoningField(field string) bool {
	switch field {
	case "reasoning_content", "reasoning", "reasoning_text":
		return true
	default:
		return false
	}
}

// textContent encodes a string as a JSON value. Marshaling a string cannot fail.
func textContent(text string) json.RawMessage {
	data, _ := json.Marshal(text)
	return data
}

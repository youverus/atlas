package responses

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// minOutputTokens is the smallest max_output_tokens the API accepts. A smaller
// value is rejected, not clamped, so the adapter raises it: a catalog entry with
// a low ceiling should still produce a request.
const minOutputTokens = 16

// Placeholders that keep a tool result paired with a non-empty output, because
// the API rejects an empty function_call_output.
const (
	emptyToolOutput = "(no tool output)"
	imageToolOutput = "(see attached image)"
)

// payload is the responses request body. Input items stay raw JSON because a
// reasoning item is a blob the server issued: re-encoding it through a struct
// with the fields this adapter happens to know would silently drop the rest.
type payload struct {
	Model  string            `json:"model"`
	Input  []json.RawMessage `json:"input"`
	Stream bool              `json:"stream"`

	// Store is always false and never omitted. This adapter keeps the
	// conversation, so a server-side copy would be state no later request
	// references.
	Store bool `json:"store"`

	MaxOutputTokens *int           `json:"max_output_tokens,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	Tools           []functionTool `json:"tools,omitempty"`
	Reasoning       *reasoning     `json:"reasoning,omitempty"`
	Include         []string       `json:"include,omitempty"`
	PromptCacheKey  string         `json:"prompt_cache_key,omitempty"`

	// ToolChoice is the same three-value vocabulary the other APIs take, and
	// this one spells it the same way: auto, none, required.
	ToolChoice string `json:"tool_choice,omitempty"`

	// ServiceTier trades latency for price on the deployments that offer
	// several queues.
	ServiceTier string `json:"service_tier,omitempty"`

	// Truncation says whether the server may drop the head of an over-long
	// conversation instead of failing the request.
	Truncation string `json:"truncation,omitempty"`

	// Text carries output shaping. It is a pointer because verbosity is the only
	// member: an empty object would still be sent.
	Text *textConfig `json:"text,omitempty"`
}

// textConfig is the output shaping object. verbosity is the one member this
// adapter knows.
type textConfig struct {
	Verbosity string `json:"verbosity,omitempty"`
}

// reasoning is the thinking dial. effort alone disables reasoning ("none");
// summary is only meaningful while reasoning runs.
type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// encryptedReasoning is the include flag that makes a store:false conversation
// possible: without it the reasoning text is not returned, and a reasoning model
// refuses a transcript whose reasoning items are missing.
const encryptedReasoning = "reasoning.encrypted_content"

// functionTool is a tool declaration. Parameters are the JSON schema as given.
type functionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// input items. A message the caller wrote carries input_text and input_image;
// an assistant message is replayed as an output message so the server sees the
// same shape it produced.
type (
	instructionItem struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	userItem struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	}
	inputText struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	inputImage struct {
		Type     string `json:"type"`
		Detail   string `json:"detail"`
		ImageURL string `json:"image_url"`
	}
	outputText struct {
		Type string `json:"type"`
		Text string `json:"text"`
		// Annotations is always present and empty: the API requires the field on
		// a replayed output message.
		Annotations []json.RawMessage `json:"annotations"`
	}
	assistantMessageItem struct {
		Type    string       `json:"type"`
		Role    string       `json:"role"`
		Content []outputText `json:"content"`
		Status  string       `json:"status"`
	}
	functionCallItem struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	functionCallOutputItem struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output any    `json:"output"`
	}
)

func (p *Provider) buildPayload(req provider.Request) (payload, error) {
	input, err := convertMessages(req)
	if err != nil {
		return payload{}, err
	}
	out := payload{
		Model:  req.Model.ID,
		Input:  input,
		Stream: true,
		Store:  false,
	}
	if req.Options.MaxTokens != nil {
		limit := *req.Options.MaxTokens
		if limit < minOutputTokens {
			limit = minOutputTokens
		}
		out.MaxOutputTokens = &limit
	}
	reasoning, include := reasoningFields(req.Model, req.Options.ThinkingLevel)
	if reasoning != nil {
		out.Reasoning = reasoning
		out.Include = include
	}
	// Temperature is left out while reasoning runs: the API rejects the
	// combination instead of ignoring it, which is the rule the other adapters
	// apply to their own thinking fields.
	if req.Options.Temperature != nil && (reasoning == nil || reasoning.Effort == "none") {
		out.Temperature = req.Options.Temperature
	}
	if tools := wire.CurrentTools(req.Messages); len(tools) > 0 {
		out.Tools = convertTools(tools)
		if choice := responsesToolChoice(p.Compat.ToolChoice); choice != "" {
			out.ToolChoice = choice
		}
	}
	// The three deployment knobs travel only when a compat table named them: an
	// empty value means the deployment default, which is what a gateway that
	// never heard of the field expects.
	out.ServiceTier = p.Compat.ServiceTier
	out.Truncation = p.Compat.Truncation
	if verbosity := p.Compat.Verbosity; verbosity != "" {
		out.Text = &textConfig{Verbosity: verbosity}
	}
	// A cache key scopes the prompt cache to one conversation; it is a hint, so
	// an absent session id simply omits it rather than inventing one.
	out.PromptCacheKey = req.Options.SessionID
	return out, nil
}

// responsesToolChoice keeps the shared vocabulary, which this API spells the
// same way, and drops a value it would not recognise.
func responsesToolChoice(value string) string {
	switch value {
	case "auto", "none", "required":
		return value
	}
	return ""
}

// reasoningFields maps a thinking level onto the reasoning object.
//
// A reasoning model is told "none" when no level was asked for, because this API
// thinks at medium effort by default: leaving the field out would turn thinking
// on for someone who asked for it off. While thinking runs, the include list
// asks for the encrypted reasoning text, without which a store:false
// conversation cannot be handed back.
func reasoningFields(model wire.Model, level string) (*reasoning, []string) {
	if !model.Reasoning {
		return nil, nil
	}
	switch level {
	case "", "off":
		return &reasoning{Effort: "none"}, nil
	default:
		return &reasoning{Effort: responsesEffort(level), Summary: "auto"}, []string{encryptedReasoning}
	}
}

// responsesEffort clamps atlas's six gears onto the effort vocabulary this API
// documents. A level it does not know takes medium, the vendor default, rather
// than being dropped.
func responsesEffort(level string) string {
	switch level {
	case "low", "medium", "high", "xhigh", "max":
		return level
	case "ultra":
		return "max"
	default:
		return "medium"
	}
}

func convertMessages(req provider.Request) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(req.Messages))
	for _, message := range req.Messages {
		switch message.Role {
		case wire.RoleSystem:
			// The prompt travels as the first input item rather than as
			// instructions, so a replay and a fresh request carry the same
			// shape. A reasoning model is addressed as the developer, which is
			// what this API prefers for its instructions.
			text := message.Text()
			if text == "" {
				continue
			}
			role := "system"
			if req.Model.Reasoning {
				role = "developer"
			}
			item, err := raw(instructionItem{Role: role, Content: text})
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		case wire.RoleUser:
			content, err := userContent(message)
			if err != nil {
				return nil, err
			}
			if len(content) == 0 {
				continue
			}
			item, err := raw(userItem{Role: "user", Content: content})
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		case wire.RoleAssistant:
			items, err := assistantItems(message)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		case wire.RoleToolResult:
			item, err := toolResultItem(req.Model, message)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		default:
			return nil, fmt.Errorf("responses: unsupported message role %q", message.Role)
		}
	}
	return out, nil
}

// assistantItems expands one assistant turn into the items the API gave it:
// reasoning first, then text, then calls, which is the order the server produced.
func assistantItems(message wire.Message) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentThinking:
			// The reasoning item is stored whole in the signature. Anything
			// else in that field — a chat-completions field name, for example —
			// is not an item, and sending it would be rejected.
			if !isReasoningItem(block.ThinkingSignature) {
				continue
			}
			out = append(out, json.RawMessage(block.ThinkingSignature))
		case wire.ContentText:
			if block.Text == "" {
				continue
			}
			item, err := raw(assistantMessageItem{
				Type:    "message",
				Role:    "assistant",
				Content: []outputText{{Type: "output_text", Text: block.Text, Annotations: []json.RawMessage{}}},
				Status:  "completed",
			})
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		case wire.ContentToolCall:
			arguments := "{}"
			if len(block.Arguments) > 0 {
				arguments = string(block.Arguments)
			}
			// The item id is left out on purpose: it belongs to the server's
			// store, and with store:false there is no such id to name.
			item, err := raw(functionCallItem{
				Type:      "function_call",
				CallID:    block.ID,
				Name:      block.Name,
				Arguments: arguments,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
	}
	return out, nil
}

func toolResultItem(model wire.Model, message wire.Message) (json.RawMessage, error) {
	if message.ToolCallID == "" {
		return nil, fmt.Errorf("responses: tool result %q has no toolCallId", message.ToolName)
	}
	output, err := toolOutput(model, message)
	if err != nil {
		return nil, err
	}
	return raw(functionCallOutputItem{Type: "function_call_output", CallID: message.ToolCallID, Output: output})
}

// toolOutput renders a tool result. A model that takes images gets a part array
// so they survive; one that does not gets text only, with a stand-in sentence
// instead of an empty string, because the API rejects an empty output.
func toolOutput(model wire.Model, message wire.Message) (any, error) {
	var texts []string
	var images []inputImage
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case wire.ContentImage:
			if block.Data == "" {
				continue
			}
			if block.MIMEType == "" {
				return nil, fmt.Errorf("responses: image block without mimeType")
			}
			images = append(images, imagePart(block))
		}
	}
	text := strings.Join(texts, "\n")

	if len(images) == 0 || !acceptsImages(model) {
		switch {
		case text != "":
			return text, nil
		case len(images) > 0:
			return imageToolOutput, nil
		default:
			return emptyToolOutput, nil
		}
	}
	parts := make([]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, inputText{Type: "input_text", Text: text})
	}
	for _, image := range images {
		parts = append(parts, image)
	}
	return parts, nil
}

func userContent(message wire.Message) ([]any, error) {
	if message.Content.IsPlain() {
		text := message.Content.PlainText()
		if text == "" {
			return nil, nil
		}
		return []any{inputText{Type: "input_text", Text: text}}, nil
	}
	var parts []any
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if block.Text == "" {
				continue
			}
			parts = append(parts, inputText{Type: "input_text", Text: block.Text})
		case wire.ContentImage:
			if block.Data == "" {
				continue
			}
			if block.MIMEType == "" {
				return nil, fmt.Errorf("responses: image block without mimeType")
			}
			parts = append(parts, imagePart(block))
		}
	}
	return parts, nil
}

func imagePart(block wire.Content) inputImage {
	return inputImage{
		Type:     "input_image",
		Detail:   "auto",
		ImageURL: "data:" + block.MIMEType + ";base64," + block.Data,
	}
}

func convertTools(tools []wire.Tool) []functionTool {
	out := make([]functionTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			// A missing schema is rejected by strict servers; an empty object is
			// accepted everywhere.
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, functionTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	return out
}

// isReasoningItem reports whether a thinking signature holds a reasoning item
// this adapter issued. A signature written by another adapter is not one, and
// replaying it as an item would fail the whole request.
func isReasoningItem(signature string) bool {
	if signature == "" || !json.Valid([]byte(signature)) {
		return false
	}
	var item struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(signature), &item); err != nil {
		return false
	}
	return item.Type == "reasoning"
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

func raw(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("responses: encode item: %w", err)
	}
	return data, nil
}

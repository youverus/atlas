package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// Defaults for the output limit. The messages API requires max_tokens, while a
// wire.Model only carries one when a catalog provided it, so the adapter needs a
// value that fits every current model instead of relying on the API.
const (
	defaultMaxTokens = 4096
	// minThinkingTokens is the smallest budget the API accepts for extended
	// thinking. Below it the adapter omits the thinking configuration rather
	// than sending a request the API rejects.
	minThinkingTokens = 1024
	// minAnswerTokens is what pi reserves for the answer when a thinking budget
	// shares the output ceiling.
	minAnswerTokens = 1024
)

// thinkingBudgets come from pi's DEFAULT_THINKING_BUDGETS, minus minimal: atlas
// starts at low. xhigh, max and ultra clamp to high because the API's
// budget-based thinking has no level above high, and adaptive effort levels
// arrive with the catalog's compat flags.
var thinkingBudgets = map[string]int{
	"low":    2048,
	"medium": 8192,
	"high":   16384,
}

// payload is the messages request body.
type payload struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	Messages  []messageParam `json:"messages"`
	System    []textBlock    `json:"system,omitempty"`
	Tools     []toolDef      `json:"tools,omitempty"`
	Stream    bool           `json:"stream"`
	Thinking  *thinking      `json:"thinking,omitempty"`

	// OutputConfig carries the effort dial: DeepSeek exposes it on its
	// Anthropic-compatible endpoint in place of a thinking budget, and adaptive
	// thinking uses it next to the thinking object.
	OutputConfig *outputConfig `json:"output_config,omitempty"`

	// ToolChoice speaks this API's vocabulary, where "required" is spelled
	// "any". It is only meaningful while tools are declared.
	ToolChoice *toolChoice `json:"tool_choice,omitempty"`

	// Temperature is omitted while thinking is enabled: the API rejects the
	// combination instead of ignoring it.
	Temperature *float64 `json:"temperature,omitempty"`
}

// messageParam carries a string for plain text and a block array otherwise, so
// content stays raw until the blocks are known.
type messageParam struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imageBlock struct {
	Type   string      `json:"type"`
	Source imageSource `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type thinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type redactedThinkingBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error,omitempty"`
}

type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`

	// Strict turns the schema from advice into a constraint. It only appears
	// once the adapter has rewritten the schema into the subset constrained
	// decoding accepts.
	Strict bool `json:"strict,omitempty"`
}

// thinking toggles the mode. budget_tokens is optional because DeepSeek
// accepts the same object on its Anthropic-compatible endpoint but ignores the
// budget, so the dialect path sends the type alone.
type thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`

	// Display is what the model reports of its own reasoning under adaptive
	// thinking. The API documents "summarized" as the default for the older
	// models, so the adapter asks for it by name and does not depend on which
	// default the deployment picked.
	Display string `json:"display,omitempty"`
}

// outputConfig replaces the thinking budget on DeepSeek's Anthropic-compatible
// endpoint: effort is a dial, not a token count. Adaptive thinking uses the
// same field for the same reason.
type outputConfig struct {
	Effort string `json:"effort"`
}

// toolChoice names a mode rather than a tool, which is what this API accepts.
type toolChoice struct {
	Type string `json:"type"`
}

// dialectDeepSeek is the Compat.ThinkingDialect value that switches the
// thinking fields to the ones DeepSeek documents.
const dialectDeepSeek = "deepseek"

// The two values DeepSeek documents for the thinking toggle. Its guide puts the
// toggle in a table cell that spans the OpenAI and the Anthropic column, and
// its compatibility page lists thinking as supported on the Messages endpoint,
// so both formats take the same object.
const (
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"
	// thinkingAdaptive asks the model to decide when and how much to think,
	// replacing the token budget with the effort dial.
	thinkingAdaptive = "adaptive"
	// thinkingDisplaySummarized spells out the default the older models already
	// use, so the request means the same thing on every deployment.
	thinkingDisplaySummarized = "summarized"
)

// buildPayload converts a provider request into the messages body. It only
// fails for a message that cannot be represented: a tool result without a call
// id, an image without a media type, or a tool call without an id.
func (p *Provider) buildPayload(req provider.Request) (payload, error) {
	out := payload{
		Model:     req.Model.ID,
		MaxTokens: maxTokens(req),
		Messages:  []messageParam{},
		Stream:    true,
	}

	// The API takes exactly one system prompt, so the replayed system message
	// becomes it: wire.CurrentSystemMessage joins later system content onto the
	// base prompt and resolves the tool set, and nothing else may carry a system
	// role.
	if head, ok := wire.CurrentSystemMessage(req.Messages); ok {
		if text := head.Content.Text(); text != "" {
			out.System = []textBlock{{Type: "text", Text: text}}
		}
	}

	messages, err := p.convertMessages(req.Messages)
	if err != nil {
		return payload{}, err
	}
	out.Messages = messages

	if tools := wire.CurrentTools(req.Messages); len(tools) > 0 {
		out.Tools = p.convertTools(tools)
		if choice := toolChoiceType(p.Compat.ToolChoice); choice != "" {
			out.ToolChoice = &toolChoice{Type: choice}
		}
	}

	switch {
	case p.Compat.ThinkingDialect == dialectDeepSeek:
		switch level := req.Options.ThinkingLevel; level {
		case "":
			// An unset level leaves the vendor default alone: DeepSeek thinks
			// at high effort unless told otherwise, so neither field travels.
		case "off":
			// The toggle is DeepSeek's documented way off, on its Anthropic
			// format as much as on its OpenAI one.
			out.Thinking = &thinking{Type: thinkingDisabled}
		default:
			out.Thinking = &thinking{Type: thinkingEnabled}
			out.OutputConfig = &outputConfig{Effort: deepSeekEffort(level)}
		}
	case p.Compat.AdaptiveThinking:
		switch level := req.Options.ThinkingLevel; {
		case level == "":
			// An unset level leaves the vendor default alone.
		case level == "off":
			out.Thinking = &thinking{Type: thinkingDisabled}
		case !req.Model.Reasoning:
			// A level means nothing to a model whose declaration says it does
			// not think; sending it anyway would be a request the API rejects.
		default:
			out.Thinking = &thinking{Type: thinkingAdaptive, Display: thinkingDisplaySummarized}
			out.OutputConfig = &outputConfig{Effort: adaptiveEffort(level)}
		}
	default:
		if settings := thinkingConfig(req); settings != nil {
			out.Thinking = settings
		}
	}

	// Thinking at work and a sampling temperature do not compose on this API,
	// so the temperature travels only while the model answers without thinking.
	if !thinkingRuns(out.Thinking) {
		out.Temperature = req.Options.Temperature
	}
	return out, nil
}

// thinkingRuns reports whether the configuration asks the model to think, which
// is the state the API refuses to combine with a temperature.
func thinkingRuns(settings *thinking) bool {
	if settings == nil {
		return false
	}
	return settings.Type == thinkingEnabled || settings.Type == thinkingAdaptive
}

// toolChoiceType translates the shared three-value vocabulary into the one this
// API uses: requiring a call is spelled "any" here.
func toolChoiceType(value string) string {
	switch value {
	case "auto":
		return "auto"
	case "none":
		return "none"
	case "required":
		return "any"
	}
	return ""
}

// maxTokens resolves the output limit. An explicit option wins over the model
// declaration, and the default covers a model that arrived without a catalog.
func maxTokens(req provider.Request) int {
	if req.Options.MaxTokens != nil && *req.Options.MaxTokens > 0 {
		return *req.Options.MaxTokens
	}
	if req.Model.MaxTokens > 0 {
		return req.Model.MaxTokens
	}
	return defaultMaxTokens
}

// deepSeekEffort maps atlas's six gears onto the effort vocabulary DeepSeek
// documents: low survives, medium/high/xhigh share high, and max and ultra share
// max. It returns "" for the levels that carry no effort: an empty level leaves
// the vendor default alone (thinking at high effort), and off is represented by
// the thinking toggle instead of an effort value.
//
// Unlike the budget path this does not require model.Reasoning: the level is
// the only signal the caller has, and DeepSeek thinks without being asked.
func deepSeekEffort(level string) string {
	switch level {
	case "", "off":
		return ""
	case "low":
		return "low"
	case "max", "ultra":
		return "max"
	default:
		// medium, high, xhigh and any unrecognised level share high, the
		// vendor default.
		return "high"
	}
}

// thinkingConfig maps a thinking level onto a token budget. It returns nil when
// thinking is off, the model does not declare reasoning, or the ceiling leaves
// no room for a budget the API accepts.
func thinkingConfig(req provider.Request) *thinking {
	level := req.Options.ThinkingLevel
	if !req.Model.Reasoning || level == "" || level == "off" {
		return nil
	}
	budget, ok := thinkingBudgets[level]
	if !ok {
		// xhigh, max, ultra and any unrecognised level clamp to high, matching
		// pi's clampReasoning.
		budget = thinkingBudgets["high"]
	}
	ceiling := maxTokens(req)
	budget = min(budget, max(0, ceiling-minAnswerTokens))
	if budget < minThinkingTokens {
		return nil
	}
	return &thinking{Type: thinkingEnabled, BudgetTokens: budget}
}

// adaptiveEffort maps atlas's gears onto the effort dial adaptive thinking
// takes. Only low and medium are distinct: pi's table sends low, medium and
// high and falls back to high for everything above, because the native xhigh
// tier exists on a few recent models only. A gear this adapter cannot express
// therefore stays at high instead of risking a rejected request.
func adaptiveEffort(level string) string {
	switch level {
	case "low":
		return "low"
	case "medium":
		return "medium"
	default:
		return "high"
	}
}

// convertTools renames Parameters to input_schema, fills the empty schema every
// current tool would otherwise send as null, and turns a strict declaration
// into a request the API accepts. It only marks a tool strict when the endpoint
// supports it, the tool asked for it, the schema survives the rewrite, and the
// request stays inside the documented ceiling of strict tools.
func (p *Provider) convertTools(tools []wire.Tool) []toolDef {
	out := make([]toolDef, 0, len(tools))
	strict := 0
	for _, tool := range tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		def := toolDef{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: parameters,
		}
		if p.Compat.StrictTools && tool.Strict && strict < maxStrictTools {
			if schema, ok := strictSchema(parameters); ok {
				def.InputSchema = schema
				def.Strict = true
				strict++
			}
		}
		out = append(out, def)
	}
	return out
}

func (p *Provider) convertMessages(messages []wire.Message) ([]messageParam, error) {
	out := make([]messageParam, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		switch message.Role {
		case wire.RoleSystem:
			// Already folded into the top-level system field.
			continue
		case wire.RoleUser:
			converted, err := convertUser(message)
			if err != nil {
				return nil, err
			}
			if converted != nil {
				out = append(out, *converted)
			}
		case wire.RoleAssistant:
			converted, err := convertAssistant(message)
			if err != nil {
				return nil, err
			}
			if converted != nil {
				out = append(out, *converted)
			}
		case wire.RoleToolResult:
			// The API pairs each tool_result block with its tool_use in the
			// previous assistant turn, so consecutive results travel together in
			// one user message.
			next, converted, err := convertToolResults(messages, i)
			if err != nil {
				return nil, err
			}
			i = next - 1
			out = append(out, *converted)
		default:
			return nil, fmt.Errorf("anthropic: unsupported message role %q", message.Role)
		}
	}
	return out, nil
}

func convertUser(message wire.Message) (*messageParam, error) {
	if message.Content.IsPlain() {
		text := message.Content.PlainText()
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return newMessage("user", jsonString(text)), nil
	}

	blocks := make([]any, 0, message.Content.Len())
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			blocks = append(blocks, textBlock{Type: "text", Text: block.Text})
		case wire.ContentImage:
			image, err := convertImage(block)
			if err != nil {
				return nil, err
			}
			if image != nil {
				blocks = append(blocks, *image)
			}
		}
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode user content: %w", err)
	}
	return newMessage("user", content), nil
}

func convertAssistant(message wire.Message) (*messageParam, error) {
	blocks := make([]any, 0, message.Content.Len())
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			blocks = append(blocks, textBlock{Type: "text", Text: block.Text})
		case wire.ContentThinking:
			if block.Redacted != nil && *block.Redacted {
				// The reasoning text was withheld; only the opaque payload can be
				// replayed, and it must go back as the same block type.
				if block.ThinkingSignature != "" {
					blocks = append(blocks, redactedThinkingBlock{Type: "redacted_thinking", Data: block.ThinkingSignature})
				}
				continue
			}
			if strings.TrimSpace(block.Thinking) == "" && block.ThinkingSignature == "" {
				continue
			}
			if block.ThinkingSignature == "" {
				// A signature is required to replay a thinking block. Without one
				// (an aborted stream, or a block from another API) the text is
				// sent as plain text instead of a request the API rejects.
				blocks = append(blocks, textBlock{Type: "text", Text: block.Thinking})
				continue
			}
			blocks = append(blocks, thinkingBlock{
				Type:      "thinking",
				Thinking:  block.Thinking,
				Signature: block.ThinkingSignature,
			})
		case wire.ContentToolCall:
			if block.ID == "" {
				return nil, fmt.Errorf("anthropic: tool call %q has no id", block.Name)
			}
			input := block.Arguments
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, toolUseBlock{
				Type:  "tool_use",
				ID:    block.ID,
				Name:  block.Name,
				Input: input,
			})
		}
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode assistant content: %w", err)
	}
	return newMessage("assistant", content), nil
}

// convertToolResults collects the run of consecutive tool results starting at
// start into one user message and returns the index just past the run.
func convertToolResults(messages []wire.Message, start int) (int, *messageParam, error) {
	blocks := make([]any, 0, len(messages)-start)
	next := start
	for ; next < len(messages) && messages[next].Role == wire.RoleToolResult; next++ {
		message := messages[next]
		if message.ToolCallID == "" {
			return 0, nil, fmt.Errorf("anthropic: tool result %q has no toolCallId", message.ToolName)
		}
		content, err := toolResultContent(message)
		if err != nil {
			return 0, nil, err
		}
		blocks = append(blocks, toolResultBlock{
			Type:      "tool_result",
			ToolUseID: message.ToolCallID,
			Content:   content,
			IsError:   message.IsError != nil && *message.IsError,
		})
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return 0, nil, fmt.Errorf("anthropic: encode tool results: %w", err)
	}
	return next, newMessage("user", encoded), nil
}

// toolResultContent renders a tool result body: a single string when it holds
// text only, a block array when it carries images. An image-only result gets a
// placeholder text block because the API rejects a block array without one.
func toolResultContent(message wire.Message) (json.RawMessage, error) {
	if message.Content.IsPlain() {
		return jsonString(message.Content.PlainText()), nil
	}

	var texts []string
	var images []imageBlock
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case wire.ContentImage:
			image, err := convertImage(block)
			if err != nil {
				return nil, err
			}
			if image != nil {
				images = append(images, *image)
			}
		}
	}

	if len(images) == 0 {
		return jsonString(strings.Join(texts, "\n")), nil
	}
	blocks := make([]any, 0, len(texts)+len(images)+1)
	if len(texts) == 0 {
		blocks = append(blocks, textBlock{Type: "text", Text: imageToolOutput})
	}
	for _, text := range texts {
		blocks = append(blocks, textBlock{Type: "text", Text: text})
	}
	for _, image := range images {
		blocks = append(blocks, image)
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode tool result content: %w", err)
	}
	return content, nil
}

func convertImage(block wire.Content) (*imageBlock, error) {
	if block.Data == "" {
		return nil, nil
	}
	if block.MIMEType == "" {
		return nil, fmt.Errorf("anthropic: image block without mimeType")
	}
	return &imageBlock{
		Type: "image",
		Source: imageSource{
			Type:      "base64",
			MediaType: block.MIMEType,
			Data:      block.Data,
		},
	}, nil
}

func newMessage(role string, content json.RawMessage) *messageParam {
	return &messageParam{Role: role, Content: content}
}

// jsonString encodes a string as a JSON value. Marshaling a string cannot fail.
func jsonString(text string) json.RawMessage {
	data, _ := json.Marshal(text)
	return data
}

// imageToolOutput is the placeholder for a tool result that carried images but
// no text, so the block array has a text block as the API requires.
const imageToolOutput = "(see attached image)"

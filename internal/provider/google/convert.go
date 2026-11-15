package google

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// Roles this API understands. A tool result also travels as a user turn,
// because the function response is the user's side of the exchange.
const (
	roleUser  = "user"
	roleModel = "model"
)

// Payload defaults. The API requires no output limit, while a wire.Model only
// carries one when a catalog provided it.
const (
	defaultMaxTokens = 8192
	// maxThinkingBudget caps the token allowance. A flash model refuses anything
	// above 24576, so a budget past that cap is lowered instead of being sent
	// and rejected: thinking a little less is better than losing the turn.
	maxThinkingBudget = 24576
)

// thinkingBudgets maps atlas's gears onto a token allowance for the models that
// take a budget rather than a level.
var thinkingBudgets = map[string]int{
	"low":    2048,
	"medium": 8192,
	"high":   16384,
	"xhigh":  maxThinkingBudget,
	"max":    maxThinkingBudget,
	"ultra":  maxThinkingBudget,
}

// levelNames maps the gears onto the names this API uses when it takes a level.
var levelNames = map[string]string{
	"minimal": "MINIMAL",
	"low":     "LOW",
	"medium":  "MEDIUM",
	"high":    "HIGH",
}

// levelThinking matches the models that take thinkingLevel. It mirrors pi's
// usesGoogleThinkingLevel: the Gemini 3 pro and flash families, the two rolling
// aliases, and Gemma 4.
var levelThinking = regexp.MustCompile(`gemini-3(?:\.\d+)?-(?:pro|flash)|gemma-?4`)

// The rolling aliases carry no version in their name, so they are matched
// exactly.
var levelThinkingAliases = map[string]bool{
	"gemini-flash-latest":      true,
	"gemini-flash-lite-latest": true,
}

// payload is the generateContent request body.
type payload struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *instruction      `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	Tools             []toolSet         `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
}

// content is one conversation turn. The role is empty for the first user turn
// in some deployments, but sending it always is accepted everywhere.
type content struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

// instruction is the system prompt, which travels beside the conversation
// because this API has no system role.
type instruction struct {
	Parts []part `json:"parts"`
}

// part is one piece of a turn. Exactly one of the payload fields is set, plus
// an optional thought signature that has to be echoed back unchanged.
type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

// functionCall is a tool call. The id is optional on older models and required
// from Gemini 3 on, so the adapter always sends one it synthesized itself.
type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   string          `json:"id,omitempty"`
}

// functionResponse is a tool result. This API distinguishes success from
// failure by the key inside response, not by a flag.
type functionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
	ID       string          `json:"id,omitempty"`
	Parts    []part          `json:"parts,omitempty"`
}

type generationConfig struct {
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *thinkingConfig `json:"thinkingConfig,omitempty"`
}

// thinkingConfig is one of the two thinking dials, never both: thinkingLevel on
// the models that take a gear, thinkingBudget on the ones that take a token
// allowance. includeThoughts asks for the summary to be streamed back.
type thinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
}

// toolSet is the wrapper this API puts around declarations.
type toolSet struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

// functionDeclaration is one tool. parameters is the OpenAPI 3.0 subset this
// API has accepted longest, which every current model reads; the newer
// parametersJsonSchema field is not universal, and atlas's tool schemas stay
// inside the subset.
type functionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolConfig forces or forbids function calling.
type toolConfig struct {
	FunctionCallingConfig functionCallingConfig `json:"functionCallingConfig"`
}

type functionCallingConfig struct {
	Mode string `json:"mode"`
}

// buildPayload converts a provider request into the generateContent body. It
// only fails for a message that cannot be represented: a tool result without a
// call id, or an image without a media type.
func (p *Provider) buildPayload(req provider.Request) (payload, error) {
	out := payload{Contents: []content{}}

	// This API has no system role: the prompt travels beside the conversation.
	// wire.CurrentSystemMessage joins later system content onto the base prompt.
	if head, ok := wire.CurrentSystemMessage(req.Messages); ok {
		if text := head.Content.Text(); text != "" {
			out.SystemInstruction = &instruction{Parts: []part{{Text: text}}}
		}
	}

	contents, err := p.convertMessages(req)
	if err != nil {
		return payload{}, err
	}
	out.Contents = contents

	generation := generationConfig{}
	if req.Options.Temperature != nil {
		// Unlike the other two APIs this one accepts a temperature next to its
		// thinking fields, so the two never have to trade places.
		generation.Temperature = req.Options.Temperature
	}
	if limit := maxTokens(req); limit > 0 {
		generation.MaxOutputTokens = limit
	}
	if settings := p.thinkingConfig(req); settings != nil {
		generation.ThinkingConfig = settings
	}
	out.GenerationConfig = &generation

	if tools := wire.CurrentTools(req.Messages); len(tools) > 0 {
		out.Tools = []toolSet{{FunctionDeclarations: convertTools(tools)}}
		if mode := functionCallingMode(p.Compat.ToolChoice); mode != "" {
			out.ToolConfig = &toolConfig{FunctionCallingConfig: functionCallingConfig{Mode: mode}}
		}
	}
	return out, nil
}

// maxTokens resolves the output limit: an explicit option wins over the model
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

// thinkingConfig picks a dial. A level of off becomes a zero budget, which is
// how this API says "do not think"; an empty level leaves the vendor default
// alone. A model that does not declare reasoning gets no configuration at all.
func (p *Provider) thinkingConfig(req provider.Request) *thinkingConfig {
	level := req.Options.ThinkingLevel
	if !req.Model.Reasoning || level == "" {
		return nil
	}
	if level == "off" {
		off := 0
		return &thinkingConfig{ThinkingBudget: &off}
	}
	if p.usesLevel(req.Model) {
		return &thinkingConfig{
			IncludeThoughts: true,
			ThinkingLevel:   levelName(level),
		}
	}
	budget := thinkingBudget(level)
	return &thinkingConfig{
		IncludeThoughts: true,
		ThinkingBudget:  &budget,
	}
}

// usesLevel reports which thinking dialect the model speaks. A compat value
// wins, because a gateway may rename a model and the id is then the only
// remaining hint.
func (p *Provider) usesLevel(model wire.Model) bool {
	switch p.Compat.ThinkingDialect {
	case DialectLevel:
		return true
	case DialectBudget:
		return false
	}
	id := strings.ToLower(model.ID)
	return levelThinking.MatchString(id) || levelThinkingAliases[id]
}

// levelName maps a gear onto the name this API uses. A gear above high stays at
// high because this dial has no tier above it, and an unrecognised level takes
// high rather than being dropped: the caller asked for thinking.
func levelName(level string) string {
	switch level {
	case "minimal", "low", "medium", "high":
		return levelNames[level]
	default:
		return levelNames["high"]
	}
}

// thinkingBudget maps a gear onto a token allowance. A gear above high takes the
// cap, because the allowance this API documents per family tops out there.
func thinkingBudget(level string) int {
	if budget, ok := thinkingBudgets[level]; ok {
		return budget
	}
	return thinkingBudgets["high"]
}

// functionCallingMode translates the shared vocabulary into the modes this API
// documents: requiring a call is spelled ANY here.
func functionCallingMode(value string) string {
	switch value {
	case "auto":
		return "AUTO"
	case "none":
		return "NONE"
	case "required":
		return "ANY"
	}
	return ""
}

// convertTools renames the schema field and fills the empty schema every
// current tool would otherwise send as null.
func convertTools(tools []wire.Tool) []functionDeclaration {
	out := make([]functionDeclaration, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, functionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	return out
}

// convertMessages turns the transcript into turns. Consecutive tool results
// share one user turn, because each function response answers a call from the
// model turn before it.
func (p *Provider) convertMessages(req provider.Request) ([]content, error) {
	out := make([]content, 0, len(req.Messages))

	for i := 0; i < len(req.Messages); i++ {
		message := req.Messages[i]
		switch message.Role {
		case wire.RoleSystem:
			// Already folded into systemInstruction.
			continue
		case wire.RoleUser:
			converted := convertUser(message)
			if converted != nil {
				out = append(out, *converted)
			}
		case wire.RoleAssistant:
			converted := convertAssistant(message, req.Model)
			if converted != nil {
				out = append(out, *converted)
			}
		case wire.RoleToolResult:
			next, merged, err := p.convertToolResults(req, i)
			if err != nil {
				return nil, err
			}
			out = append(out, merged...)
			i = next - 1
		default:
			return nil, fmt.Errorf("google: unsupported message role %q", message.Role)
		}
	}
	return out, nil
}

func convertUser(message wire.Message) *content {
	if message.Content.IsPlain() {
		text := message.Content.PlainText()
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return &content{Role: roleUser, Parts: []part{{Text: text}}}
	}

	parts := make([]part, 0, message.Content.Len())
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			parts = append(parts, part{Text: block.Text})
		case wire.ContentImage:
			if block.Data == "" || block.MIMEType == "" {
				continue
			}
			parts = append(parts, part{InlineData: &inlineData{MIMEType: block.MIMEType, Data: block.Data}})
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &content{Role: roleUser, Parts: parts}
}

// convertAssistant replays a model turn. A thought signature has to travel back
// on the part that carried it, otherwise the model loses the reasoning chain and
// ends its turn early; the frame stores the three kinds separately, so each is
// restored onto its own kind of part.
func convertAssistant(message wire.Message, model wire.Model) *content {
	parts := make([]part, 0, message.Content.Len())
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if strings.TrimSpace(block.Text) == "" && block.TextSignature == "" {
				continue
			}
			parts = append(parts, part{Text: block.Text, ThoughtSignature: replayable(block.TextSignature, message, model)})
		case wire.ContentThinking:
			if strings.TrimSpace(block.Thinking) == "" && block.ThinkingSignature == "" {
				continue
			}
			parts = append(parts, part{
				Text:             block.Thinking,
				Thought:          true,
				ThoughtSignature: replayable(block.ThinkingSignature, message, model),
			})
		case wire.ContentToolCall:
			arguments := block.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage("{}")
			}
			parts = append(parts, part{
				FunctionCall: &functionCall{
					Name: block.Name,
					Args: arguments,
					ID:   callID(block.ID),
				},
				ThoughtSignature: replayable(block.ThoughtSignature, message, model),
			})
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &content{Role: roleModel, Parts: parts}
}

// replayable keeps a signature only when it came from this provider and model
// and looks like the base64 blob this API issued. A signature from elsewhere is
// meaningless to the model and a malformed one is rejected outright.
func replayable(signature string, message wire.Message, model wire.Model) string {
	if signature == "" || message.Provider != model.Provider || message.Model != model.ID {
		return ""
	}
	if len(signature)%4 != 0 || !base64Signature.MatchString(signature) {
		return ""
	}
	return signature
}

// base64Signature matches the standard alphabet with its padding, which is the
// only shape this API's TYPE_BYTES field accepts.
var base64Signature = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// callID normalizes a tool call id into the character set this API accepts and
// keeps it inside the documented length. The same value goes back on the
// function response, which is how the two are paired.
func callID(id string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, id)
	if len(cleaned) > maxCallIDLength {
		return cleaned[:maxCallIDLength]
	}
	return cleaned
}

// maxCallIDLength is the documented ceiling on a function call id.
const maxCallIDLength = 64

// convertToolResults collects the run of consecutive tool results starting at
// start into one user turn and returns the index just past the run. A result
// that carries images the model cannot nest inside its response contributes a
// second turn, because that is the only shape those models accept.
func (p *Provider) convertToolResults(req provider.Request, start int) (int, []content, error) {
	parts := make([]part, 0, len(req.Messages)-start)
	var loose []part
	next := start
	for ; next < len(req.Messages) && req.Messages[next].Role == wire.RoleToolResult; next++ {
		message := req.Messages[next]
		id := message.ToolCallID
		if id == "" {
			return 0, nil, fmt.Errorf("google: tool result %q has no toolCallId", message.ToolName)
		}
		response, images, err := toolResponse(req, message)
		if err != nil {
			return 0, nil, err
		}
		block := part{FunctionResponse: &functionResponse{
			Name:     message.ToolName,
			Response: response,
			ID:       callID(id),
		}}
		if len(images) > 0 && supportsMultimodalFunctionResponse(req.Model) {
			block.FunctionResponse.Parts = images
			images = nil
		}
		parts = append(parts, block)
		loose = append(loose, images...)
	}

	out := []content{{Role: roleUser, Parts: parts}}
	if len(loose) > 0 {
		out = append(out, content{Role: roleUser, Parts: append([]part{{Text: imageToolOutput}}, loose...)})
	}
	return next, out, nil
}

// toolResponse renders the body of a tool result. This API tells success from
// failure by the key inside the response object.
func toolResponse(req provider.Request, message wire.Message) (json.RawMessage, []part, error) {
	failed := message.IsError != nil && *message.IsError

	texts := []string{}
	images := []part{}
	if message.Content.IsPlain() {
		if text := message.Content.PlainText(); text != "" {
			texts = append(texts, text)
		}
	} else {
		for _, block := range message.Content.Items() {
			switch block.Type {
			case wire.ContentText:
				if block.Text != "" {
					texts = append(texts, block.Text)
				}
			case wire.ContentImage:
				if !acceptsImages(req.Model) || block.Data == "" || block.MIMEType == "" {
					continue
				}
				images = append(images, part{InlineData: &inlineData{MIMEType: block.MIMEType, Data: block.Data}})
			}
		}
	}

	text := strings.Join(texts, "\n")
	if text == "" && len(images) > 0 {
		text = imageToolOutput
	}
	key := "output"
	if failed {
		key = "error"
	}
	encoded, err := json.Marshal(map[string]string{key: text})
	if err != nil {
		return nil, nil, fmt.Errorf("google: encode tool response: %w", err)
	}
	return encoded, images, nil
}

// supportsMultimodalFunctionResponse reports whether a function response may
// carry images. Gemini 3 and later allows it; older models and the other model
// families served through this API do not.
func supportsMultimodalFunctionResponse(model wire.Model) bool {
	version := geminiMajorVersion(model.ID)
	if version == 0 {
		return true
	}
	return version >= 3
}

// acceptsImages reports whether the model takes images at all.
func acceptsImages(model wire.Model) bool {
	for _, input := range model.Input {
		if input == "image" {
			return true
		}
	}
	// A model declaration without an input list is assumed to take text only
	// where the caller said so; an empty list means the catalog said nothing.
	return len(model.Input) == 0
}

// geminiMajorVersion reads the version out of a Gemini model id, or returns 0
// for a family whose ids carry none.
func geminiMajorVersion(id string) int {
	match := geminiVersion.FindStringSubmatch(strings.ToLower(id))
	if match == nil {
		return 0
	}
	version := 0
	for _, digit := range match[1] {
		version = version*10 + int(digit-'0')
	}
	return version
}

var geminiVersion = regexp.MustCompile(`^gemini(?:-live)?-(\d+)`)

// imageToolOutput is the placeholder for a tool result that carried images but
// no text, so the response object has a string in it.
const imageToolOutput = "Tool result image:"

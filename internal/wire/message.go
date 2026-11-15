package wire

import (
	"encoding/json"
	"time"
)

// Role values used by pi's Message union.
type Role string

const (
	RoleSystem     Role = "system"
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// StopReason mirrors packages/ai/src/types.ts.
type StopReason string

const (
	StopPending  StopReason = "pending"
	StopStop     StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopError    StopReason = "error"
	StopAborted  StopReason = "aborted"
	StopDeferred StopReason = "deferred"
)

// Error codes name a failure the adapter could identify, so a caller can react
// to it without reading the message text. Empty means unclassified: the
// ErrorMessage is all there is.
//
// pi has no such field. It decides everything from the error text, which is why
// its overflow recovery works and the Go kernel had none (§9.3 A13). A code that
// travels on the message is the narrower contract: the layer that can see the
// HTTP status names the failure once, and no caller parses English twice.
const (
	// ErrorCodeContextOverflow means the request did not fit the model's
	// context window. The turn can be recovered by summarizing and retrying.
	ErrorCodeContextOverflow = "context_overflow"
)

// Message is the wire form of every agent message. Roles share one struct so
// that a session file round trips without losing fields; unused fields stay
// omitted.
type Message struct {
	Role    Role        `json:"role"`
	Content ContentList `json:"content"`

	// System messages. Sections patch by name: null removes a section.
	Sections     map[string]*string `json:"sections,omitempty"`
	ToolsAdded   []Tool             `json:"toolsAdded,omitempty"`
	ToolsRemoved []ToolReference    `json:"toolsRemoved,omitempty"`

	// Common metadata. Milliseconds since the Unix epoch.
	Timestamp int64 `json:"timestamp"`

	// Assistant messages.
	API                   string          `json:"api,omitempty"`
	Provider              string          `json:"provider,omitempty"`
	Model                 string          `json:"model,omitempty"`
	ResponseModel         string          `json:"responseModel,omitempty"`
	ResponseID            string          `json:"responseId,omitempty"`
	ProviderThinkingLevel string          `json:"providerThinkingLevel,omitempty"`
	ThinkingLevel         string          `json:"thinkingLevel,omitempty"`
	Usage                 *Usage          `json:"usage,omitempty"`
	StopReason            StopReason      `json:"stopReason,omitempty"`
	ErrorMessage          string          `json:"errorMessage,omitempty"`
	ErrorCode             string          `json:"errorCode,omitempty"`
	RawStopReason         string          `json:"rawStopReason,omitempty"`
	EndTurn               *bool           `json:"endTurn,omitempty"`
	DurationMs            *int64          `json:"durationMs,omitempty"`
	Diagnostics           json.RawMessage `json:"diagnostics,omitempty"`

	// Tool result messages.
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`

	// Interrupted marks a tool result whose call was cut off: it started, the
	// reader stopped the turn, and Content holds what the tool had produced by
	// then. The flag is what makes "where the turn stopped" a fact in the file
	// instead of a sentence to be matched against the text — a caller asking
	// "can this turn be continued, and were tools left half done" reads this
	// rather than parsing "Operation aborted".
	Interrupted       bool            `json:"interrupted,omitempty"`
	Details           json.RawMessage `json:"details,omitempty"`
	IsError           *bool           `json:"isError,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	NestedCalls       json.RawMessage `json:"nestedCalls,omitempty"`
}

// Tool is a tool declaration sent to the model.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`

	// Strict asks the endpoint to validate the arguments against Parameters
	// instead of treating the schema as advice. It is the tool's own statement:
	// a schema with an optional property is exactly what a caller may want to
	// keep loose, so the declaration decides and the adapter only carries it.
	// An adapter drops the flag when the endpoint cannot honour it, because a
	// rejected request loses the tool call the declaration promised.
	Strict bool `json:"strict,omitempty"`

	// ConstrainedSampling is pi's richer form of the same request: a JSON
	// document naming the sampling strategy and how strongly to prefer it. No
	// adapter reads it yet; Strict is the boolean they act on.
	ConstrainedSampling json.RawMessage `json:"constrainedSampling,omitempty"`
}

// ToolReference removes a previously declared tool.
type ToolReference struct {
	Name string `json:"name"`
}

// Now is the millisecond timestamp pi uses for messages.
func Now() int64 { return time.Now().UnixMilli() }

// Ptr returns a pointer to v, for optional wire fields.
func Ptr[T any](v T) *T { return &v }

// SystemMessage builds the leading system message. Content stays the string
// form, matching pi's initial system message.
func SystemMessage(text string) Message {
	return Message{Role: RoleSystem, Content: NewTextList(text), Timestamp: 0}
}

// UserMessage builds a user message from plain text.
func UserMessage(text string) Message {
	return Message{Role: RoleUser, Content: NewTextList(text), Timestamp: Now()}
}

// UserMessageBlocks builds a user message from content blocks.
func UserMessageBlocks(blocks ...Content) Message {
	return Message{Role: RoleUser, Content: NewBlocks(blocks...), Timestamp: Now()}
}

// AssistantMessage builds a pending assistant message with the given blocks.
func AssistantMessage(blocks ...Content) Message {
	return Message{Role: RoleAssistant, Content: NewBlocks(blocks...), Timestamp: Now(), StopReason: StopPending}
}

// ToolResultMessage builds a tool result. isError is written explicitly so the
// field survives a round trip.
func ToolResultMessage(callID, toolName string, isError bool, blocks ...Content) Message {
	return Message{
		Role:       RoleToolResult,
		Content:    NewBlocks(blocks...),
		ToolCallID: callID,
		ToolName:   toolName,
		IsError:    Ptr(isError),
		Timestamp:  Now(),
	}
}

// Text returns the concatenated text blocks.
func (m Message) Text() string { return m.Content.Text() }

// ToolCalls returns the tool call blocks in source order.
func (m Message) ToolCalls() []Content {
	var calls []Content
	for _, block := range m.Content.Items() {
		if block.Type == ContentToolCall {
			calls = append(calls, block)
		}
	}
	return calls
}

// IsErrorResult reports whether a tool result is marked as an error.
func (m Message) IsErrorResult() bool {
	return m.IsError != nil && *m.IsError
}

// Clone copies the message and its content list for event snapshots.
func (m Message) Clone() Message {
	m.Content = m.Content.Clone()
	return m
}

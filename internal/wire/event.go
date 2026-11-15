package wire

import "encoding/json"

// Assistant stream event types, mirroring packages/ai/src/types.ts.
const (
	EventStart         = "start"
	EventTextStart     = "text_start"
	EventTextDelta     = "text_delta"
	EventTextEnd       = "text_end"
	EventThinkingStart = "thinking_start"
	EventThinkingDelta = "thinking_delta"
	EventThinkingEnd   = "thinking_end"
	EventToolCallStart = "toolcall_start"
	EventToolCallDelta = "toolcall_delta"
	EventToolCallEnd   = "toolcall_end"
	EventDone          = "done"
	EventError         = "error"
)

// AssistantEvent is one provider stream event. The union is flattened so the
// JSON matches pi's event protocol exactly; ContentIndex is a pointer because
// index 0 is meaningful and must not be dropped by omitempty.
type AssistantEvent struct {
	Type         string   `json:"type"`
	ContentIndex *int     `json:"contentIndex,omitempty"`
	Delta        string   `json:"delta,omitempty"`
	Content      string   `json:"content,omitempty"`
	Partial      *Message `json:"partial,omitempty"`
	ToolCall     *Content `json:"toolCall,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Message      *Message `json:"message,omitempty"`
	Error        *Message `json:"error,omitempty"`
}

// Agent event types, mirroring packages/agent/src/types.ts, plus the two
// compaction events packages/coding-agent/src/core/agent-session.ts adds on top.
const (
	AgentEventStart           = "agent_start"
	AgentEventEnd             = "agent_end"
	AgentEventTurnStart       = "turn_start"
	AgentEventTurnEnd         = "turn_end"
	AgentEventMessageStart    = "message_start"
	AgentEventMessageUpdate   = "message_update"
	AgentEventMessageEnd      = "message_end"
	AgentEventToolExecStart   = "tool_execution_start"
	AgentEventToolExecUpdate  = "tool_execution_update"
	AgentEventToolExecEnd     = "tool_execution_end"
	AgentEventCompactionStart = "compaction_start"
	AgentEventCompactionEnd   = "compaction_end"
)

// Compaction reasons. Overflow is a recovery attempt: the request did not fit
// the window, so the failed turn is set aside, the branch is summarized, and the
// turn is tried once more.
const (
	CompactionReasonManual    = "manual"
	CompactionReasonThreshold = "threshold"
	CompactionReasonOverflow  = "overflow"
)

// CompactionResult is the payload of a compaction_end event, mirroring pi's
// CompactionResult. EstimatedTokensAfter is a pointer because a reported zero
// differs from "not reported".
type CompactionResult struct {
	Summary              string          `json:"summary"`
	FirstKeptEntryID     string          `json:"firstKeptEntryId"`
	TokensBefore         int             `json:"tokensBefore"`
	EstimatedTokensAfter *int            `json:"estimatedTokensAfter,omitempty"`
	Usage                *Usage          `json:"usage,omitempty"`
	Details              json.RawMessage `json:"details,omitempty"`
}

// AgentEvent is one agent lifecycle event. Fields that only apply to some
// event types stay empty and are omitted from JSON.
type AgentEvent struct {
	Type string `json:"type"`

	Message        *Message        `json:"message,omitempty"`
	Messages       []Message       `json:"messages,omitempty"`
	AssistantEvent *AssistantEvent `json:"assistantMessageEvent,omitempty"`
	ToolResults    []Message       `json:"toolResults,omitempty"`

	ToolCallID    string          `json:"toolCallId,omitempty"`
	ToolName      string          `json:"toolName,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
	PartialResult json.RawMessage `json:"partialResult,omitempty"`
	// Result carries a tool result or a marshaled CompactionResult, depending on
	// the event type: pi's protocol uses the same key for both.
	Result     json.RawMessage `json:"result,omitempty"`
	IsError    *bool           `json:"isError,omitempty"`
	DurationMs *int64          `json:"durationMs,omitempty"`

	// compaction_start and compaction_end.
	Reason       string `json:"reason,omitempty"`
	Aborted      *bool  `json:"aborted,omitempty"`
	WillRetry    *bool  `json:"willRetry,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

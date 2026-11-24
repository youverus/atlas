package rpc

import (
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// command is one decoded JSONL command.
//
// pi describes its commands as a discriminated union. The Go side decodes every
// command into one struct because the protocol is closed and the fields do not
// collide: a command reads the fields it needs and ignores the rest. Nothing
// here is trusted until a handler validates it.
type command struct {
	ID   *string `json:"id"`
	Type string  `json:"type"`

	// prompt, steer, follow_up.
	Message string         `json:"message"`
	Images  []wire.Content `json:"images"`

	// prompt: how to queue the message when a turn is already running.
	StreamingBehavior string `json:"streamingBehavior"`

	// set_model.
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`

	// set_thinking_level.
	Level string `json:"level"`

	// set_steering_mode and set_follow_up_mode.
	Mode string `json:"mode"`

	// get_entries: return only the entries after this one.
	Since *string `json:"since"`

	// switch_session.
	SessionPath string `json:"sessionPath"`

	// fork.
	EntryID string `json:"entryId"`

	// set_session_name.
	Name string `json:"name"`

	// compact: extra instructions for the summary.
	CustomInstructions string `json:"customInstructions"`

	// set_auto_compaction. A pointer separates "false" from "omitted", because
	// omitting it is a client bug rather than a request to disable.
	Enabled *bool `json:"enabled"`
}

// response is the envelope every command answers with. A success carries data
// or nothing; a failure carries the error and never data, so a client can
// distinguish "empty result" from "failed".
type response struct {
	ID      *string `json:"id,omitempty"`
	Type    string  `json:"type"`
	Command string  `json:"command"`
	Success bool    `json:"success"`
	Data    any     `json:"data,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// dispositionStarted means the prompt began a run; the events follow.
const dispositionStarted = "started"

// dispositionQueued means the message was buffered for the running or next run.
const dispositionQueued = "queued"

type dispositionResult struct {
	Disposition string `json:"disposition"`
}

type clearQueueResult struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
	// both are always arrays: a client iterates them without a nil check.
}

type newSessionResult struct {
	Cancelled bool `json:"cancelled"`
}

// sessionState mirrors pi's RpcSessionState. Every field is always reported, so a
// client that reads one gets the truth instead of an omitted key.
type sessionState struct {
	Model                 wire.Model `json:"model"`
	ThinkingLevel         string     `json:"thinkingLevel"`
	IsStreaming           bool       `json:"isStreaming"`
	IsCompacting          bool       `json:"isCompacting"`
	SteeringMode          string     `json:"steeringMode"`
	FollowUpMode          string     `json:"followUpMode"`
	SessionFile           string     `json:"sessionFile,omitempty"`
	SessionID             string     `json:"sessionId"`
	SessionName           string     `json:"sessionName,omitempty"`
	AutoCompactionEnabled bool       `json:"autoCompactionEnabled"`
	MessageCount          int        `json:"messageCount"`
	PendingMessageCount   int        `json:"pendingMessageCount"`
}

type messagesResult struct {
	Messages []wire.Message `json:"messages"`
}

type entriesResult struct {
	Entries []session.Entry `json:"entries"`
	LeafID  *string         `json:"leafId"`
}

// jsonTreeNode mirrors pi's SessionTreeNode. The session package keeps labels
// alongside its own nodes, so the transport shapes them here rather than
// carrying JSON tags into the store.
type jsonTreeNode struct {
	Entry          session.Entry  `json:"entry"`
	Children       []jsonTreeNode `json:"children"`
	Label          *string        `json:"label,omitempty"`
	LabelTimestamp string         `json:"labelTimestamp,omitempty"`
}

type treeResult struct {
	Tree   []jsonTreeNode `json:"tree"`
	LeafID *string        `json:"leafId"`
}

// lastAssistantText has no field when the conversation has no assistant text,
// which is what pi sends: the property is undefined and JSON drops it.
type lastAssistantText struct {
	Text string `json:"text,omitempty"`
}

type commandsResult struct {
	Commands []any `json:"commands"`
}

type thinkingLevelsResult struct {
	Levels []string `json:"levels"`
}

// availableModelsResult is pi's shape for the switch list: whole Model objects,
// because a client renders the list from this answer and would otherwise have to
// ask again for the names.
type availableModelsResult struct {
	Models []wire.Model `json:"models"`
}

// modelCycleResult reports both halves of the setting a switch changes, matching
// pi's ModelCycleResult. isScoped is always false here: a cycle walks everything
// the kernel was configured with, so it never narrows to a subset.
type modelCycleResult struct {
	Model         wire.Model `json:"model"`
	ThinkingLevel string     `json:"thinkingLevel"`
	IsScoped      bool       `json:"isScoped"`
}

// thinkingCycleResult carries the single level a cycle landed on, which is pi's
// shape for cycle_thinking_level.
type thinkingCycleResult struct {
	Level string `json:"level"`
}

type tokenStats struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
	Total      int `json:"total"`
}

// contextUsageStats mirrors pi's ContextUsage. Tokens and Percent have no
// omitempty because null is an explicit value immediately after compaction.
type contextUsageStats struct {
	Tokens        *int     `json:"tokens"`
	ContextWindow int      `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
}

// sessionStatsResult mirrors pi's SessionStats. Cost has no omitempty so a zero
// cost session still emits "cost": 0, matching TypeScript's number field.
type sessionStatsResult struct {
	SessionFile       string             `json:"sessionFile,omitempty"`
	SessionID         string             `json:"sessionId"`
	UserMessages      int                `json:"userMessages"`
	AssistantMessages int                `json:"assistantMessages"`
	ToolCalls         int                `json:"toolCalls"`
	ToolResults       int                `json:"toolResults"`
	TotalMessages     int                `json:"totalMessages"`
	Tokens            tokenStats         `json:"tokens"`
	Cost              float64            `json:"cost"`
	ContextUsage      *contextUsageStats `json:"contextUsage,omitempty"`
}

// forkResult says the branch was made. Nothing is handed back with it: a fork
// is taken after a reply, and that reply is in the new session already, so there
// is no prompt to put in the input box.
type forkResult struct {
	Cancelled bool `json:"cancelled"`
}

type cloneResult struct {
	Cancelled bool `json:"cancelled"`
}

// forkMessageItem is one answer a session can be branched from. The name is
// pi's command, but the rows are answers: atlas branches after a reply, not
// before a prompt, so a caller asking what it can fork from gets replies.
//
// Turn counts prompts from 1, which is how a frontend groups them.
type forkMessageItem struct {
	EntryID string `json:"entryId"`
	Turn    int    `json:"turn"`
	Text    string `json:"text"`
}

type forkMessagesResult struct {
	Messages []forkMessageItem `json:"messages"`
}

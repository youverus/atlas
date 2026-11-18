// Package trajectory folds a recorded session into the rows a debugger walks.
//
// The window's transcript is a reading view: one block per prompt, one per
// answer, one per call, with attachment bodies and thinking dropped so a
// conversation stays readable. A reader asking "where did this turn go wrong"
// needs the opposite — every recorded fact, in file order, with enough
// provenance to open the line it came from. The two views disagree on purpose,
// which is why this package does not call transcript.Replay: that function
// takes messages, and by then the entry kinds and their timestamps are gone.
//
// Nothing here truncates. The tool result is the evidence, and a row that hides
// half of it is the row that sends the reader back to the file by hand. A
// caller that needs a ceiling puts one on its own output.
package trajectory

import (
	"encoding/json"
	"sort"
	"strings"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// Kind is one row's business type. The set is closed so a window can switch on
// it; a recorded line that is none of these is KindOther and keeps its raw JSON,
// which is better than dropping a fact this package has no reading for.
type Kind string

const (
	// KindPrompt is a message the reader typed. It also opens a turn.
	KindPrompt Kind = "prompt"
	// KindAssistant is one model round: its answer, its reasoning, its usage.
	KindAssistant Kind = "assistant"
	// KindTool is one call, anchored where the model emitted it and carrying
	// the outcome once the matching result arrives.
	KindTool Kind = "tool"
	// KindSystem is a system message, which a compaction may rewrite.
	KindSystem Kind = "system"
	// KindCompaction is a context compaction: the moment history was replaced
	// by a summary. The row that answers "why did it forget the constraint".
	KindCompaction Kind = "compaction"
	// KindBranchSummary is the epitaph of a path that was abandoned.
	KindBranchSummary Kind = "branch_summary"
	// KindContextEdit is a recorded replacement of an earlier message.
	KindContextEdit Kind = "context_edit"
	// KindModelChange is a provider or model switch.
	KindModelChange Kind = "model_change"
	// KindThinkingChange is a reasoning-level switch.
	KindThinkingChange Kind = "thinking_level_change"
	// KindLabel is a bookmark the reader placed on an entry.
	KindLabel Kind = "label"
	// KindSessionInfo is the conversation's own name being recorded or changed.
	// It never reached the model, so it stays out of prompts and rounds; the row
	// exists because a rename is a real moment in a session's life.
	KindSessionInfo Kind = "session_info"
	// KindUsage is model spend that never entered the context, such as a cache
	// warm-up or the summarisation call of a compaction.
	KindUsage Kind = "usage"
	// KindOther is a line this package has no reading for.
	KindOther Kind = "other"
)

// Row is one recorded fact, flattened for a reader that inspects rather than
// reads. The fields are shared across kinds and mean one thing each; which ones
// a kind fills is part of its contract:
//
//	prompt            Text, Turn
//	assistant         Text, Thinking, Usage, Model, StopReason through Error/IsError
//	tool              Tool, Args, Result, DurationMs, IsError, Ref (call id)
//	system            Text
//	compaction        Text (summary), Ref (first kept entry), TokensBefore, Usage
//	branch_summary    Text (summary), Ref (the entry branched away from)
//	context_edit      Text (the replacement), Ref (the edited entry)
//	model_change      Model
//	thinking_level_change Text (the level)
//	label             Text (the label), Ref (the labeled entry)
//	session_info      Text (the name)
//	usage             Ref (what the spend was for), Model, Usage, Text (the note)
//	other             Ref (the role or entry type), Text (the raw JSON)
//
// The struct carries its own JSON names rather than being mirrored by a second
// type in httpapi: unlike transcript.Block it has no unexported bookkeeping to
// hide, so a mirror would only be a place for the two to drift apart.
type Row struct {
	// Index is the row's position in this fold, and the only unique key: a
	// round's calls share their round's Seq, so Seq cannot identify a row.
	Index int `json:"index"`
	// Seq is the line the entry occupies in the file: the header is line 1, so
	// the first entry is line 2. A file with a line the loader skipped is the
	// one exception, since later entries move up while this numbering does not.
	Seq int `json:"seq"`
	// EntryID and ParentID are the tree links the row came from.
	EntryID  string `json:"entryId"`
	ParentID string `json:"parentId,omitempty"`
	// Turn numbers the prompt the row belongs to, starting at 1. Rows recorded
	// before any prompt are 0. Grouping is a fact here, not the window's guess
	// at role adjacency.
	Turn int  `json:"turn"`
	Kind Kind `json:"kind"`
	// Timestamp is the entry's own ISO 8601 time, which is the only source for
	// how long anything took when the same fact is not also recorded as a
	// duration.
	Timestamp string `json:"timestamp"`
	// DurationMs is a recorded wall-clock duration: the tool call, or the round.
	DurationMs int64 `json:"durationMs,omitempty"`
	// IsError marks a failed call or a failed round.
	IsError bool `json:"isError,omitempty"`
	// Interrupted marks work the reader stopped rather than work that failed.
	Interrupted bool `json:"interrupted,omitempty"`

	// Tool is the tool name. Ref is the entry or call this row points at.
	Tool string `json:"tool,omitempty"`
	Ref  string `json:"ref,omitempty"`
	// Text is the row's body: the prompt, the answer, the summary, the label.
	Text string `json:"text,omitempty"`
	// Thinking is the round's reasoning, which the transcript drops.
	Thinking string `json:"thinking,omitempty"`
	// Error is the provider's own failure sentence, when there was one.
	Error string `json:"error,omitempty"`
	// Args and Result are the call's arguments and output, in full.
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	// Model is "provider/model" for a round, a switch, or a spend.
	Model string `json:"model,omitempty"`
	// Usage is the recorded token and cost accounting for this row.
	Usage *wire.Usage `json:"usage,omitempty"`
	// TokensBefore is how large the context was when a compaction replaced it.
	TokensBefore int64 `json:"tokensBefore,omitempty"`
}

// Fold walks the branch that ends at leafID and returns one row per recorded
// fact, in file order.
//
// A nil leaf means the session has no active leaf, so nothing is on the branch
// and the answer is empty rather than the whole file. That is
// session.BuildPath's reading, and it is kept here: a caller that lost the leaf
// should see an empty view, not a plausible one built from a guess.
//
// A round is one row. Its calls follow it as their own rows, in the order the
// model emitted them, rather than being interleaved with the text that
// surrounded them — usage and timing belong to the round, and a row that carries
// them should not also contain the call.
func Fold(entries []session.Entry, leafID *string) []Row {
	path := session.BuildPath(entries, leafID)
	folder := folder{
		rows:  make([]Row, 0, len(path)),
		seq:   make(map[string]int, len(entries)),
		calls: map[string]int{},
	}
	for i := range entries {
		// The header occupies line 1, so entry i sits on line i+2.
		folder.seq[entries[i].ID] = i + 2
	}
	for _, entry := range path {
		folder.add(entry)
	}
	return folder.rows
}

// folder is the fold's working state: the rows so far, where each entry sits in
// the file, the turn being numbered, and where each call's row was drawn so its
// result lands there instead of on a line of its own.
type folder struct {
	rows  []Row
	seq   map[string]int
	turn  int
	calls map[string]int
}

// push appends a row and returns the index it landed at.
func (f *folder) push(row Row) int {
	row.Index = len(f.rows)
	f.rows = append(f.rows, row)
	return row.Index
}

// add folds one entry. Everything a row needs that the entry does not carry
// itself — the turn it belongs to, the seq of a message's calls — is filled here.
func (f *folder) add(entry session.Entry) {
	base := Row{
		Seq:       f.seq[entry.ID],
		EntryID:   entry.ID,
		ParentID:  deref(entry.ParentID),
		Turn:      f.turn,
		Timestamp: entry.Timestamp,
	}
	switch entry.Type {
	case session.TypeMessage:
		f.addMessage(base, entry)
	case session.TypeCompaction:
		base.Kind = KindCompaction
		base.Text = entry.Summary
		base.Ref = entry.FirstKeptEntryID
		base.TokensBefore = number(entry.TokensBefore)
		base.Usage = decodeUsage(entry.Usage)
		f.push(base)
	case session.TypeBranchSummary:
		base.Kind = KindBranchSummary
		base.Text = entry.Summary
		base.Ref = entry.FromID
		base.Usage = decodeUsage(entry.Usage)
		f.push(base)
	case session.TypeContextEdit:
		base.Kind = KindContextEdit
		base.Ref = entry.TargetID
		base.Text = string(entry.Replacement)
		f.push(base)
	case session.TypeModelChange:
		base.Kind = KindModelChange
		base.Model = join(entry.Provider, entry.ModelID)
		f.push(base)
	case session.TypeThinkingLevelChange:
		base.Kind = KindThinkingChange
		base.Text = entry.ThinkingLevel
		f.push(base)
	case session.TypeLabel:
		base.Kind = KindLabel
		base.Ref = entry.TargetID
		base.Text = deref(entry.Label)
		f.push(base)
	case session.TypeSessionInfo:
		base.Kind = KindSessionInfo
		base.Text = entry.Name
		f.push(base)
	case session.TypeUsage:
		base.Kind = KindUsage
		base.Ref = entry.Kind
		base.Model = join(entry.Provider, entry.Model)
		base.Text = entry.Note
		base.Usage = decodeUsage(entry.Usage)
		f.push(base)
	default:
		base.Kind = KindOther
		base.Ref = entry.Type
		base.Text = rawLine(entry)
		f.push(base)
	}
}

// addMessage folds the entry kinds that hold a wire message: prompts, rounds,
// results, and the system messages a compaction rewrites.
func (f *folder) addMessage(base Row, entry session.Entry) {
	var message wire.Message
	if err := json.Unmarshal(entry.Message, &message); err != nil {
		base.Kind = KindOther
		base.Ref = "message"
		base.Text = string(entry.Message)
		f.push(base)
		return
	}
	switch message.Role {
	case wire.RoleUser:
		f.turn++
		base.Turn = f.turn
		base.Kind = KindPrompt
		// The attachment bodies stay: what the model was handed under a prompt
		// is exactly what a reader debugging a wrong answer needs to see.
		base.Text = message.Content.Text()
		f.push(base)
	case wire.RoleAssistant:
		base.Kind = KindAssistant
		base.Text = message.Content.Text()
		base.Thinking = thinkingText(message)
		base.Model = join(message.Provider, message.Model)
		base.Usage = message.Usage
		base.DurationMs = millis(message.DurationMs)
		base.Error = message.ErrorMessage
		base.IsError = message.StopReason == wire.StopError
		base.Interrupted = message.StopReason == wire.StopAborted
		f.push(base)
		f.addCalls(base, message)
	case wire.RoleToolResult:
		f.addResult(base, message)
	case wire.RoleSystem:
		base.Kind = KindSystem
		base.Text = systemText(message)
		f.push(base)
	default:
		// Roles this kernel does not model (bashExecution, custom) still held
		// something the model saw, so the row keeps the message verbatim.
		base.Kind = KindOther
		base.Ref = string(message.Role)
		base.Text = string(entry.Message)
		f.push(base)
	}
}

// addCalls draws one row per call the round emitted, remembering where so the
// result can be written onto it.
func (f *folder) addCalls(round Row, message wire.Message) {
	for _, call := range message.ToolCalls() {
		// The call is anchored on the round that emitted it, and carries none
		// of the round's own data: a reader looking at a call wants the call.
		toolRow := Row{
			Seq:       round.Seq,
			EntryID:   round.EntryID,
			ParentID:  round.ParentID,
			Turn:      round.Turn,
			Kind:      KindTool,
			Timestamp: round.Timestamp,
			Tool:      call.Name,
			Ref:       call.ID,
			Args:      string(call.Arguments),
		}
		index := f.push(toolRow)
		if call.ID != "" {
			f.calls[call.ID] = index
		}
	}
}

// addResult writes the outcome onto its call's row. A call and its result are
// two records but one event, which is the same reading the transcript uses when
// it draws them as one line.
func (f *folder) addResult(base Row, message wire.Message) {
	if index, ok := f.calls[message.ToolCallID]; ok && message.ToolCallID != "" {
		row := &f.rows[index]
		row.Result = message.Content.Text()
		row.IsError = message.IsErrorResult()
		row.Interrupted = message.Interrupted
		row.DurationMs = millis(message.DurationMs)
		return
	}
	// A result whose call is gone: a fork can keep one and drop the other, and
	// hiding the survivor would look like the work never ran.
	base.Kind = KindTool
	base.Tool = message.ToolName
	base.Ref = message.ToolCallID
	base.Result = message.Content.Text()
	base.IsError = message.IsErrorResult()
	base.Interrupted = message.Interrupted
	base.DurationMs = millis(message.DurationMs)
	f.push(base)
}

// thinkingText joins the round's reasoning blocks. The transcript drops them
// because a replayed reasoning block has no duration to report; a reader
// debugging a wrong decision is asking what the model was thinking, so here
// they stay.
func thinkingText(message wire.Message) string {
	var parts []string
	for _, block := range message.Content.Items() {
		if block.Type == wire.ContentThinking && block.Thinking != "" {
			parts = append(parts, block.Thinking)
		}
	}
	return strings.Join(parts, "\n")
}

// systemText describes a system message by what it changed. A rewritten prompt
// is shown in full; a section patch or a tool roster change is named, because
// the identity of what moved is the fact and the body is usually unchanged.
func systemText(message wire.Message) string {
	var parts []string
	if text := strings.TrimRight(message.Content.Text(), "\n"); text != "" {
		parts = append(parts, text)
	}
	var sections []string
	for name, value := range message.Sections {
		if value == nil {
			sections = append(sections, "-"+name)
			continue
		}
		sections = append(sections, "+"+name)
	}
	sort.Strings(sections)
	if len(sections) > 0 {
		parts = append(parts, "sections: "+strings.Join(sections, " "))
	}
	var tools []string
	for _, tool := range message.ToolsAdded {
		tools = append(tools, "+"+tool.Name)
	}
	for _, tool := range message.ToolsRemoved {
		tools = append(tools, "-"+tool.Name)
	}
	sort.Strings(tools)
	if len(tools) > 0 {
		parts = append(parts, "tools: "+strings.Join(tools, " "))
	}
	return strings.Join(parts, "\n")
}

// rawLine is the entry as it appears on disk, for the cases this package has no
// reading for. An entry built in memory has no line, so it is marshalled back
// into one: the body of such an entry is not always in the message field, and a
// row with no body would hide a fact rather than show an unfamiliar one.
func rawLine(entry session.Entry) string {
	if raw := entry.Raw(); len(raw) > 0 {
		return string(raw)
	}
	if len(entry.Message) > 0 {
		return string(entry.Message)
	}
	rebuilt, err := json.Marshal(entry)
	if err != nil {
		return ""
	}
	return string(rebuilt)
}

// decodeUsage reads recorded accounting. An entry carrying none stays nil,
// which is the difference between "no tokens" and "not recorded".
func decodeUsage(raw json.RawMessage) *wire.Usage {
	if len(raw) == 0 {
		return nil
	}
	var parsed wire.Usage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return &parsed
}

// number reads an optional count. A damaged value reads as zero rather than
// failing the fold: one broken line must not hide the session behind it.
func number(value json.Number) int64 {
	if value == "" {
		return 0
	}
	parsed, err := value.Int64()
	if err != nil {
		return 0
	}
	return parsed
}

func millis(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// join spells a provider and a model as one field, since every reader that wants
// one of them wants to know which the other was.
func join(provider, model string) string {
	switch {
	case provider == "":
		return model
	case model == "":
		return provider
	}
	return provider + "/" + model
}

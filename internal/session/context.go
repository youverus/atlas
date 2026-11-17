package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"pi-go/internal/wire"
)

// Summary wrappers. These strings reach the model verbatim, so they are part of
// the compatibility contract rather than presentation.
const (
	CompactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	CompactionSummarySuffix = "\n</summary>"
	BranchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSummarySuffix     = "</summary>"
)

// DefaultThinkingLevel is what a session without a thinking_level_change entry
// reports.
//
// It is a gear rather than "off", which is where pi starts. A reasoning model
// that answers without reasoning is a failure the reader notices only by
// distrusting the answer, and the default is the only setting most turns ever
// see: an agent that has to be told to think looks like one that cannot. A
// model that declares no reasoning ignores this, because the adapters send no
// thinking configuration for it, and "off" stays one selection away everywhere.
const DefaultThinkingLevel = "medium"

// ModelRef identifies the model in effect at the end of the active branch.
type ModelRef struct {
	Provider string
	ModelID  string
}

// ProjectedEntry pairs an entry with the messages it contributes. A nil Messages
// slice means the entry contributes nothing (state-only, omitted, or an older
// compaction inside the kept range).
type ProjectedEntry struct {
	Source   Entry
	Messages []wire.Message
}

// Projection is the model context plus the provenance it came from.
type Projection struct {
	Entries       []ProjectedEntry
	Messages      []wire.Message
	ThinkingLevel string
	Model         *ModelRef
}

// Context is what a provider request needs from the session.
type Context struct {
	Messages      []wire.Message
	ThinkingLevel string
	Model         *ModelRef
}

// messageMeta is the union of the message roles a session may contain. The four
// LLM roles are decoded as wire.Message; the rest exist only in sessions and are
// converted to user messages, which is what pi's convertToLlm does.
type messageMeta struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Timestamp int64           `json:"timestamp"`

	CustomType string `json:"customType"`
	Display    *bool  `json:"display"`

	Summary      string      `json:"summary"`
	FromID       string      `json:"fromId"`
	TokensBefore json.Number `json:"tokensBefore"`

	Command            string `json:"command"`
	Output             string `json:"output"`
	ExitCode           *int   `json:"exitCode"`
	Cancelled          bool   `json:"cancelled"`
	Truncated          bool   `json:"truncated"`
	FullOutputPath     string `json:"fullOutputPath"`
	ExcludeFromContext bool   `json:"excludeFromContext"`
}

// EntryMessages converts one entry into the messages it contributes to model
// context. State-only entries (model change, thinking level, labels, usage,
// custom) contribute none.
func EntryMessages(entry Entry) ([]wire.Message, error) {
	switch entry.Type {
	case TypeMessage:
		return messageEntryMessages(entry)
	case TypeCustomMessage:
		content, err := contentValue(entry.Content)
		if err != nil {
			return nil, fmt.Errorf("session: custom message %s: %w", entry.ID, err)
		}
		return []wire.Message{{
			Role:      wire.RoleUser,
			Content:   content,
			Timestamp: millis(entry.Timestamp),
		}}, nil
	case TypeBranchSummary:
		if entry.Summary == "" {
			return nil, nil
		}
		return []wire.Message{summaryMessage(
			BranchSummaryPrefix+entry.Summary+BranchSummarySuffix,
			millis(entry.Timestamp),
		)}, nil
	case TypeCompaction:
		var messages []wire.Message
		if len(entry.SystemMessage) > 0 {
			var system wire.Message
			if err := json.Unmarshal(entry.SystemMessage, &system); err != nil {
				return nil, fmt.Errorf("session: compaction system message %s: %w", entry.ID, err)
			}
			messages = append(messages, system)
		}
		return append(messages, summaryMessage(
			CompactionSummaryPrefix+entry.Summary+CompactionSummarySuffix,
			millis(entry.Timestamp),
		)), nil
	default:
		return nil, nil
	}
}

func messageEntryMessages(entry Entry) ([]wire.Message, error) {
	var meta messageMeta
	if err := json.Unmarshal(entry.Message, &meta); err != nil {
		return nil, fmt.Errorf("session: message entry %s: %w", entry.ID, err)
	}
	switch meta.Role {
	case string(wire.RoleSystem), string(wire.RoleUser), string(wire.RoleAssistant), string(wire.RoleToolResult):
		var message wire.Message
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			return nil, fmt.Errorf("session: message entry %s: %w", entry.ID, err)
		}
		// Old and hand-edited files can carry a null content; pi repairs it so a
		// provider never sees a null body.
		if isNull(meta.Content) {
			if message.Role == wire.RoleSystem {
				message.Content = wire.NewTextList("")
			} else {
				message.Content = wire.NewBlocks()
			}
		}
		return []wire.Message{message}, nil

	case "custom":
		content, err := contentValue(meta.Content)
		if err != nil {
			return nil, fmt.Errorf("session: custom message entry %s: %w", entry.ID, err)
		}
		return []wire.Message{{Role: wire.RoleUser, Content: content, Timestamp: meta.Timestamp}}, nil

	case "branchSummary":
		return []wire.Message{summaryMessage(
			BranchSummaryPrefix+meta.Summary+BranchSummarySuffix,
			meta.Timestamp,
		)}, nil

	case "compactionSummary":
		return []wire.Message{summaryMessage(
			CompactionSummaryPrefix+meta.Summary+CompactionSummarySuffix,
			meta.Timestamp,
		)}, nil

	case "bashExecution":
		if meta.ExcludeFromContext {
			return nil, nil
		}
		return []wire.Message{summaryMessage(bashExecutionText(meta), meta.Timestamp)}, nil

	default:
		// Unknown roles (extension additions, future pi versions) are ignored
		// rather than sent to a provider that cannot accept them.
		return nil, nil
	}
}

// bashExecutionText renders a `!` command for the model, matching pi's wording.
func bashExecutionText(meta messageMeta) string {
	var out strings.Builder
	out.WriteString("Ran `" + meta.Command + "`\n")
	if meta.Output != "" {
		out.WriteString("```\n" + meta.Output + "\n```")
	} else {
		out.WriteString("(no output)")
	}
	if meta.Cancelled {
		out.WriteString("\n\n(command cancelled)")
	} else if meta.ExitCode != nil && *meta.ExitCode != 0 {
		out.WriteString(fmt.Sprintf("\n\nCommand exited with code %d", *meta.ExitCode))
	}
	if meta.Truncated && meta.FullOutputPath != "" {
		out.WriteString("\n\n[Output truncated. Full output: " + meta.FullOutputPath + "]")
	}
	return out.String()
}

// summaryMessage wraps text in the string content form, which is what pi's
// conversion produces for a single text block.
func summaryMessage(text string, timestamp int64) wire.Message {
	return wire.Message{Role: wire.RoleUser, Content: wire.NewTextList(text), Timestamp: timestamp}
}

// contentValue decodes a session content field, preserving the string form.
func contentValue(raw json.RawMessage) (wire.ContentList, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || isNull(trimmed) {
		return wire.NewBlocks(), nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return wire.ContentList{}, err
		}
		return wire.NewTextList(text), nil
	}
	var blocks []wire.Content
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return wire.ContentList{}, err
	}
	return wire.NewBlocks(blocks...), nil
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// BuildPath walks from the leaf to the root and returns the path in root-first
// order. A nil leaf means the session has no active leaf (a fresh root or a
// reset), so the path is empty; an id that is not in the entries falls back to
// the last entry, as pi does. A broken or cyclic parent chain stops at the break
// instead of looping.
func BuildPath(entries []Entry, leafID *string) []Entry {
	if leafID == nil {
		return nil
	}
	index := BuildIndex(entries)
	leaf := index[*leafID]
	if leaf == nil {
		if len(entries) == 0 {
			return nil
		}
		leaf = &entries[len(entries)-1]
	}

	var path []Entry
	seen := map[string]bool{}
	current := leaf
	for current != nil {
		if seen[current.ID] {
			break
		}
		seen[current.ID] = true
		path = append(path, *current)
		if current.ParentID == nil {
			break
		}
		current = index[*current.ParentID]
	}
	// Reverse into root-first order.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// BuildIndex maps entry ids to entries. Later duplicates win, matching pi's Map
// semantics when a file was hand-edited.
func BuildIndex(entries []Entry) map[string]*Entry {
	index := make(map[string]*Entry, len(entries))
	for i := range entries {
		index[entries[i].ID] = &entries[i]
	}
	return index
}

// BuildContextEntries applies the compaction rules to the active path: the newest
// compaction stands in for everything before it, followed by the kept range and
// then the entries after the compaction.
func BuildContextEntries(entries []Entry, leafID *string) []Entry {
	path := BuildPath(entries, leafID)

	compactionAt := -1
	for i := range path {
		if path[i].Type == TypeCompaction {
			compactionAt = i
		}
	}
	if compactionAt < 0 {
		return path
	}

	compaction := path[compactionAt]
	context := make([]Entry, 0, len(path))
	context = append(context, compaction)
	kept := false
	for i := 0; i < compactionAt; i++ {
		entry := path[i]
		if entry.ID == compaction.FirstKeptEntryID {
			kept = true
		}
		if !kept {
			continue
		}
		// An old system prompt inside the kept range would be replayed as a
		// mid-conversation system message; pi drops those.
		if entry.Type == TypeMessage && messageRole(entry) == string(wire.RoleSystem) {
			continue
		}
		context = append(context, entry)
	}
	context = append(context, path[compactionAt+1:]...)
	return context
}

// BuildProjection resolves the active path into model messages with provenance.
func BuildProjection(entries []Entry, leafID *string) (*Projection, error) {
	path := BuildPath(entries, leafID)
	projection := &Projection{ThinkingLevel: DefaultThinkingLevel}
	for _, entry := range path {
		switch entry.Type {
		case TypeThinkingLevelChange:
			projection.ThinkingLevel = entry.ThinkingLevel
		case TypeModelChange:
			projection.Model = &ModelRef{Provider: entry.Provider, ModelID: entry.ModelID}
		case TypeMessage:
			if messageRole(entry) != string(wire.RoleAssistant) {
				continue
			}
			var assistant struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			if err := json.Unmarshal(entry.Message, &assistant); err == nil {
				projection.Model = &ModelRef{Provider: assistant.Provider, ModelID: assistant.Model}
			}
		}
	}

	contextEntries := BuildContextEntries(entries, leafID)
	edits := map[string]json.RawMessage{}
	for _, entry := range contextEntries {
		if entry.Type == TypeContextEdit {
			edits[entry.TargetID] = entry.Replacement
		}
	}
	projection.Entries = make([]ProjectedEntry, 0, len(contextEntries))
	for i, entry := range contextEntries {
		projected := ProjectedEntry{Source: entry}
		// An older compaction can stay inside the kept range because its raw id
		// is referenced; only the newest one contributes a checkpoint.
		newestCompaction := entry.Type != TypeCompaction || i == 0
		if newestCompaction {
			messages, err := EntryMessages(entry)
			if err != nil {
				return nil, err
			}
			projected.Messages = applyContextEdit(messages, edits[entry.ID])
		}
		projection.Entries = append(projection.Entries, projected)
		projection.Messages = append(projection.Messages, projected.Messages...)
	}
	return projection, nil
}

// BuildContext resolves the active path into what a provider request needs.
func BuildContext(entries []Entry, leafID *string) (*Context, error) {
	projection, err := BuildProjection(entries, leafID)
	if err != nil {
		return nil, err
	}
	return &Context{
		Messages:      projection.Messages,
		ThinkingLevel: projection.ThinkingLevel,
		Model:         projection.Model,
	}, nil
}

// applyContextEdit replaces the content of an entry's messages, or drops them
// when the replacement is null. Content edits are append-only: the original
// entry keeps its bytes so the history stays auditable.
func applyContextEdit(messages []wire.Message, replacement json.RawMessage) []wire.Message {
	if len(replacement) == 0 {
		return messages
	}
	if isNull(replacement) {
		return nil
	}
	var edit struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(replacement, &edit); err != nil {
		return messages
	}
	out := make([]wire.Message, len(messages))
	copy(out, messages)
	for i := range out {
		switch out[i].Role {
		case wire.RoleUser, wire.RoleAssistant, wire.RoleToolResult:
		default:
			continue
		}
		content, err := contentValue(edit.Content)
		if err != nil {
			continue
		}
		// Assistant and tool result content is always a block array on the wire.
		if out[i].Role != wire.RoleUser && content.IsPlain() {
			content = wire.NewBlocks(wire.TextContent(content.PlainText()))
		}
		out[i].Content = content
	}
	return out
}

// messageRole reads only the role discriminator of a message entry.
func messageRole(entry Entry) string {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(entry.Message, &probe); err != nil {
		return ""
	}
	return probe.Role
}

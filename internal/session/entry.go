package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CurrentVersion is the session format version this package writes. pi migrated
// v1 → v2 (tree ids) and v2 → v3 (hookMessage → custom); both are handled on load.
const CurrentVersion = 3

// Entry type values. These strings are the on-disk format and cannot change.
const (
	TypeMessage             = "message"
	TypeThinkingLevelChange = "thinking_level_change"
	TypeModelChange         = "model_change"
	TypeUsage               = "usage"
	TypeCompaction          = "compaction"
	TypeBranchSummary       = "branch_summary"
	TypeCustom              = "custom"
	TypeCustomMessage       = "custom_message"
	TypeContextEdit         = "context_edit"
	TypeLabel               = "label"
	TypeSessionInfo         = "session_info"
)

// Header is the first line of a session file: `"type": "session"`.
type Header struct {
	Version       int    `json:"version,omitempty"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	Cwd           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`

	extra map[string]json.RawMessage
}

// Type reports the header discriminator, always "session".
func (h Header) Type() string { return "session" }

// Entry is one line after the header. One struct holds every entry type, the
// same trade-off as wire.Content: JSON round-trip fidelity matters more than
// type purity, because a session written here must load in pi unchanged.
//
// Fields that this version never interprets (compaction details, usage blocks,
// extension data) stay as raw JSON instead of typed fields, so unknown content
// survives a load/save cycle. Unknown keys and unknown entry types are kept in
// extra and raw respectively.
type Entry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`

	// message entries. Message keeps the original JSON so custom roles
	// (bashExecution, custom, branchSummary, compactionSummary) round trip too.
	Message json.RawMessage `json:"message,omitempty"`

	// thinking_level_change.
	ThinkingLevel string `json:"thinkingLevel,omitempty"`

	// model_change and usage.
	Provider string          `json:"provider,omitempty"`
	ModelID  string          `json:"modelId,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	Model    string          `json:"model,omitempty"`
	Usage    json.RawMessage `json:"usage,omitempty"`
	Note     string          `json:"note,omitempty"`

	// compaction and branch_summary.
	Summary          string          `json:"summary,omitempty"`
	FirstKeptEntryID string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     json.Number     `json:"tokensBefore,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
	FromHook         *bool           `json:"fromHook,omitempty"`
	SystemMessage    json.RawMessage `json:"systemMessage,omitempty"`
	FromID           string          `json:"fromId,omitempty"`

	// custom and custom_message.
	CustomType string          `json:"customType,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Display    *bool           `json:"display,omitempty"`

	// context_edit and label.
	TargetID    string          `json:"targetId,omitempty"`
	Replacement json.RawMessage `json:"replacement,omitempty"`
	Label       *string         `json:"label,omitempty"`

	// session_info.
	Name string `json:"name,omitempty"`

	// raw is the exact line this entry was decoded from. It is used verbatim
	// until the entry is mutated, which keeps pi's key order (and any field this
	// struct does not know about) intact.
	raw   json.RawMessage
	extra map[string]json.RawMessage
}

// knownKeys lists the JSON keys Entry claims. Everything else lands in extra.
var knownKeys = map[string]bool{
	"type": true, "id": true, "parentId": true, "timestamp": true,
	"message": true, "thinkingLevel": true, "provider": true, "modelId": true,
	"kind": true, "model": true, "usage": true, "note": true,
	"summary": true, "firstKeptEntryId": true, "tokensBefore": true,
	"details": true, "fromHook": true, "systemMessage": true, "fromId": true,
	"customType": true, "data": true, "content": true, "display": true,
	"targetId": true, "replacement": true, "label": true, "name": true,
}

// Raw returns the entry as it appears on disk, when it has not been modified
// since loading. Callers that need a stable document should marshal instead.
func (e Entry) Raw() json.RawMessage { return e.raw }

// invalidate drops the cached line so a mutation is reflected on the wire.
func (e *Entry) invalidate() { e.raw = nil }

// SetParentID rewrites the tree link, which fork and branch extraction rely on.
func (e *Entry) SetParentID(id *string) {
	e.ParentID = id
	e.invalidate()
}

// SetFirstKeptEntryID rewrites a compaction boundary.
func (e *Entry) SetFirstKeptEntryID(id string) {
	e.FirstKeptEntryID = id
	e.invalidate()
}

func decodeHeader(line []byte) (Header, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return Header{}, err
	}
	var header Header
	if err := decodeInto(fields, "version", &header.Version); err != nil {
		return Header{}, err
	}
	if err := decodeInto(fields, "id", &header.ID); err != nil {
		return Header{}, err
	}
	if err := decodeInto(fields, "timestamp", &header.Timestamp); err != nil {
		return Header{}, err
	}
	if err := decodeInto(fields, "cwd", &header.Cwd); err != nil {
		return Header{}, err
	}
	if err := decodeInto(fields, "parentSession", &header.ParentSession); err != nil {
		return Header{}, err
	}
	header.extra = rest(fields, map[string]bool{
		"type": true, "version": true, "id": true, "timestamp": true, "cwd": true, "parentSession": true,
	})
	return header, nil
}

// MarshalJSON writes the header pi expects, preserving unknown keys.
func (h Header) MarshalJSON() ([]byte, error) {
	fields := map[string]any{}
	for key, value := range h.extra {
		fields[key] = value
	}
	fields["type"] = "session"
	fields["version"] = h.Version
	fields["id"] = h.ID
	fields["timestamp"] = h.Timestamp
	fields["cwd"] = h.Cwd
	if h.ParentSession != "" {
		fields["parentSession"] = h.ParentSession
	}
	return json.Marshal(fields)
}

func decodeEntry(line []byte) (Entry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return Entry{}, err
	}
	entry := Entry{raw: append(json.RawMessage(nil), line...)}
	var err error
	for _, field := range []struct {
		key string
		dst any
	}{
		{"type", &entry.Type},
		{"id", &entry.ID},
		{"parentId", &entry.ParentID},
		{"timestamp", &entry.Timestamp},
		{"message", &entry.Message},
		{"thinkingLevel", &entry.ThinkingLevel},
		{"provider", &entry.Provider},
		{"modelId", &entry.ModelID},
		{"kind", &entry.Kind},
		{"model", &entry.Model},
		{"usage", &entry.Usage},
		{"note", &entry.Note},
		{"summary", &entry.Summary},
		{"firstKeptEntryId", &entry.FirstKeptEntryID},
		{"tokensBefore", &entry.TokensBefore},
		{"details", &entry.Details},
		{"fromHook", &entry.FromHook},
		{"systemMessage", &entry.SystemMessage},
		{"fromId", &entry.FromID},
		{"customType", &entry.CustomType},
		{"data", &entry.Data},
		{"content", &entry.Content},
		{"display", &entry.Display},
		{"targetId", &entry.TargetID},
		{"replacement", &entry.Replacement},
		{"label", &entry.Label},
		{"name", &entry.Name},
	} {
		if err = decodeInto(fields, field.key, field.dst); err != nil {
			return Entry{}, fmt.Errorf("session: entry %s: %w", field.key, err)
		}
	}
	entry.extra = rest(fields, knownKeys)
	return entry, nil
}

// MarshalJSON writes the cached line for unmodified entries and rebuilds it from
// typed fields otherwise. Unknown fields from extra are merged back in.
func (e Entry) MarshalJSON() ([]byte, error) {
	if e.raw != nil {
		return e.raw, nil
	}
	fields := map[string]any{}
	for key, value := range e.extra {
		fields[key] = value
	}
	fields["type"] = e.Type
	fields["id"] = e.ID
	fields["timestamp"] = e.Timestamp
	if e.ParentID != nil {
		fields["parentId"] = *e.ParentID
	} else {
		fields["parentId"] = nil
	}
	switch e.Type {
	case TypeMessage:
		setRaw(fields, "message", e.Message)
	case TypeThinkingLevelChange:
		fields["thinkingLevel"] = e.ThinkingLevel
	case TypeModelChange:
		fields["provider"] = e.Provider
		fields["modelId"] = e.ModelID
	case TypeUsage:
		fields["kind"] = e.Kind
		fields["provider"] = e.Provider
		fields["model"] = e.Model
		setRaw(fields, "usage", e.Usage)
		if e.Note != "" {
			fields["note"] = e.Note
		}
	case TypeCompaction:
		fields["summary"] = e.Summary
		fields["firstKeptEntryId"] = e.FirstKeptEntryID
		if e.TokensBefore != "" {
			fields["tokensBefore"] = e.TokensBefore
		}
		setRaw(fields, "details", e.Details)
		setRaw(fields, "usage", e.Usage)
		setRaw(fields, "systemMessage", e.SystemMessage)
		if e.FromHook != nil {
			fields["fromHook"] = *e.FromHook
		}
	case TypeBranchSummary:
		fields["fromId"] = e.FromID
		fields["summary"] = e.Summary
		setRaw(fields, "details", e.Details)
		setRaw(fields, "usage", e.Usage)
		if e.FromHook != nil {
			fields["fromHook"] = *e.FromHook
		}
	case TypeCustom:
		fields["customType"] = e.CustomType
		setRaw(fields, "data", e.Data)
	case TypeCustomMessage:
		fields["customType"] = e.CustomType
		setRaw(fields, "content", e.Content)
		if e.Display != nil {
			fields["display"] = *e.Display
		}
		setRaw(fields, "details", e.Details)
	case TypeContextEdit:
		fields["targetId"] = e.TargetID
		if len(e.Replacement) > 0 {
			fields["replacement"] = e.Replacement
		} else {
			fields["replacement"] = nil
		}
	case TypeLabel:
		fields["targetId"] = e.TargetID
		if e.Label != nil {
			fields["label"] = *e.Label
		}
	case TypeSessionInfo:
		if e.Name != "" {
			fields["name"] = e.Name
		}
	}
	return json.Marshal(fields)
}

// decodeInto decodes one key when present, leaving dst untouched otherwise.
func decodeInto(fields map[string]json.RawMessage, key string, dst any) error {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// rest returns the fields this struct does not claim.
func rest(fields map[string]json.RawMessage, known map[string]bool) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	for key, value := range fields {
		if known[key] {
			continue
		}
		if out == nil {
			out = map[string]json.RawMessage{}
		}
		out[key] = value
	}
	return out
}

func setRaw(fields map[string]any, key string, value json.RawMessage) {
	if len(value) == 0 {
		return
	}
	fields[key] = value
}

// millis converts a session timestamp to Unix milliseconds. Session entries store
// ISO 8601 strings while messages store milliseconds, and both must agree.
func millis(timestamp string) int64 {
	if timestamp == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

// formatTimestamp renders the ISO 8601 form pi writes, always UTC with millis.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// sanitizeName replaces runs of line breaks with one space so a display name
// stays one line, matching pi's `replace(/[\r\n]+/g, " ").trim()`.
func sanitizeName(name string) string {
	var out strings.Builder
	pendingBreak := false
	for _, r := range name {
		if r == '\r' || r == '\n' {
			pendingBreak = true
			continue
		}
		if pendingBreak {
			out.WriteByte(' ')
			pendingBreak = false
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

package wire

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Content block types. These mirror packages/ai/src/types.ts.
const (
	ContentText     = "text"
	ContentThinking = "thinking"
	ContentToolCall = "toolCall"
	ContentImage    = "image"
)

// Content is one block in a message. It is a flat struct rather than an
// interface because JSON round-trip fidelity matters more than type purity:
// sessions written by pi must load here, and sessions written here must load
// in pi.
type Content struct {
	Type string `json:"type"`

	// Text blocks.
	Text          string `json:"text,omitempty"`
	TextSignature string `json:"textSignature,omitempty"`

	// Thinking blocks.
	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	Redacted          *bool  `json:"redacted,omitempty"`

	// Tool call blocks.
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	Namespace        string          `json:"namespace,omitempty"`

	// Image blocks.
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// TextContent builds a text block.
func TextContent(text string) Content {
	return Content{Type: ContentText, Text: text}
}

// ThinkingContent builds a thinking block.
func ThinkingContent(thinking, signature string) Content {
	return Content{Type: ContentThinking, Thinking: thinking, ThinkingSignature: signature}
}

// ToolCallContent builds a tool call block. arguments must be a JSON object.
func ToolCallContent(id, name string, arguments json.RawMessage) Content {
	return Content{Type: ContentToolCall, ID: id, Name: name, Arguments: arguments}
}

// ImageContent builds an image block from base64 data.
func ImageContent(data, mimeType string) Content {
	return Content{Type: ContentImage, Data: data, MIMEType: mimeType}
}

// ContentList is a message content value. pi accepts either a plain string or
// an array of blocks; the original shape is preserved so a session survives a
// load/save round trip unchanged.
type ContentList struct {
	items   []Content
	plain   string
	isPlain bool
}

// NewTextList is the string form: `"content": "text"`.
func NewTextList(text string) ContentList {
	return ContentList{plain: text, isPlain: true}
}

// NewBlocks is the array form: `"content": [ ... ]`.
func NewBlocks(blocks ...Content) ContentList {
	if blocks == nil {
		blocks = []Content{}
	}
	return ContentList{items: blocks}
}

// IsPlain reports whether the value came from (or will encode as) a JSON string.
func (l ContentList) IsPlain() bool { return l.isPlain }

// PlainText returns the string form, or "" when the value is a block array.
func (l ContentList) PlainText() string {
	if l.isPlain {
		return l.plain
	}
	return ""
}

// Items returns the block form. A string value becomes one text block.
func (l ContentList) Items() []Content {
	if !l.isPlain {
		return l.items
	}
	return []Content{{Type: ContentText, Text: l.plain}}
}

// Len returns the number of blocks in the normalized form.
func (l ContentList) Len() int {
	if l.isPlain {
		return 1
	}
	return len(l.items)
}

// Text joins every text block with a newline.
func (l ContentList) Text() string {
	if l.isPlain {
		return l.plain
	}
	var parts []string
	for _, block := range l.items {
		if block.Type == ContentText && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Ensure grows the list so index i exists, and returns a pointer to it. A
// string value is converted to a block array first.
func (l *ContentList) Ensure(i int) *Content {
	if i < 0 {
		panic("wire: negative content index")
	}
	if l.isPlain {
		l.items = []Content{{Type: ContentText, Text: l.plain}}
		l.plain = ""
		l.isPlain = false
	}
	for len(l.items) <= i {
		l.items = append(l.items, Content{})
	}
	return &l.items[i]
}

// At returns a pointer to block i, or nil when it does not exist.
func (l *ContentList) At(i int) *Content {
	if l.isPlain || i < 0 || i >= len(l.items) {
		return nil
	}
	return &l.items[i]
}

// Append adds a block, converting a string value to a block array first.
func (l *ContentList) Append(block Content) {
	if l.isPlain {
		l.items = []Content{{Type: ContentText, Text: l.plain}}
		l.plain = ""
		l.isPlain = false
	}
	l.items = append(l.items, block)
}

// Clone deep-enough copies the list for an event snapshot. Arguments byte
// slices are shared; the frame replaces rather than mutates them.
func (l ContentList) Clone() ContentList {
	if l.isPlain {
		return ContentList{plain: l.plain, isPlain: true}
	}
	items := make([]Content, len(l.items))
	copy(items, l.items)
	return ContentList{items: items}
}

// MarshalJSON preserves the original shape: string values stay strings.
func (l ContentList) MarshalJSON() ([]byte, error) {
	if l.isPlain {
		return json.Marshal(l.plain)
	}
	if l.items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(l.items)
}

// UnmarshalJSON accepts either a string or an array of blocks.
func (l *ContentList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*l = ContentList{}
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		*l = ContentList{plain: text, isPlain: true}
		return nil
	}
	var items []Content
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return err
	}
	*l = ContentList{items: items}
	return nil
}

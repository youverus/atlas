package compact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// chain builds a parent-linked session path. Prepare walks the parent links to
// resolve the branch, so entries have to be chained rather than listed.
type chain struct {
	list  []session.Entry
	last  string
	clock int
}

func newChain() *chain { return &chain{} }

func (c *chain) append(entry session.Entry) *chain {
	c.clock++
	entry.ID = entryName(len(c.list))
	if entry.Timestamp == "" {
		entry.Timestamp = "2026-10-24T00:00:00.000Z"
	}
	if c.last != "" {
		parent := c.last
		entry.ParentID = &parent
	}
	c.last = entry.ID
	c.list = append(c.list, entry)
	return c
}

func entryName(index int) string {
	return "entry-" + string(rune('a'+index))
}

func (c *chain) user(text string) *chain {
	return c.append(session.Entry{Type: session.TypeMessage, Message: encode(wire.UserMessage(text))})
}

func (c *chain) assistant(blocks ...wire.Content) *chain {
	return c.append(session.Entry{Type: session.TypeMessage, Message: encode(wire.AssistantMessage(blocks...))})
}

func (c *chain) toolResult(callID, name string, blocks ...wire.Content) *chain {
	return c.append(session.Entry{Type: session.TypeMessage, Message: encode(wire.ToolResultMessage(callID, name, false, blocks...))})
}

// message appends a caller-built message, which is how tests attach usage or a
// particular stop reason without re-implementing the helpers above.
func (c *chain) message(message wire.Message) *chain {
	return c.append(session.Entry{Type: session.TypeMessage, Message: encode(message)})
}

func (c *chain) thinkingLevel(level string) *chain {
	return c.append(session.Entry{Type: session.TypeThinkingLevelChange, ThinkingLevel: level})
}

func (c *chain) compaction(summary, firstKeptEntryID string) *chain {
	return c.append(session.Entry{
		Type:             session.TypeCompaction,
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     json.Number("4000"),
	})
}

// contextEdit removes an earlier entry from the context, which is how a dropped
// message is recorded: a null replacement projects to no message at all.
func (c *chain) contextEdit(targetID string) *chain {
	return c.append(session.Entry{
		Type:        session.TypeContextEdit,
		TargetID:    targetID,
		Replacement: json.RawMessage("null"),
	})
}

func (c *chain) entries() []session.Entry { return c.list }

func encode(message wire.Message) json.RawMessage {
	encoded, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return encoded
}

func mustJSON(t *testing.T, message wire.Message) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	return encoded
}

// filler returns text whose estimate is close to the requested number of tokens
// (ASCII characters count four to a token).
func filler(tokens int) string {
	return strings.Repeat("x", tokens*4)
}

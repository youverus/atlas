package tui

import (
	"strings"
	"time"

	"pi-go/internal/wire"
)

// Apply folds one agent event into the transcript. It is the whole of the
// interface's knowledge about the event protocol: everything above it deals in
// blocks.
//
// The event stream is the same one the inline renderer consumes, so the shapes
// match by construction: an answer is text deltas until the message ends, a
// thinking block is the reasoning before it, and a tool call is a start and an
// end with a call id between them — plus any number of progress updates riding
// between those two, which rewrite the running call's tail.
func (t *Transcript) Apply(event wire.AgentEvent) {
	switch event.Type {
	case wire.AgentEventMessageStart:
		// A new assistant message starts a fresh answer: the previous block
		// must not keep accumulating text. The prompt and the tool results also
		// start messages, and those close nothing.
		if event.Message != nil && event.Message.Role == wire.RoleAssistant {
			t.closeRole(RoleThinking, "Thought for")
			t.closeRole(RoleAnswer, "")
			t.streamed = false
		}
	case wire.AgentEventMessageUpdate:
		t.applyAssistant(event.AssistantEvent)
	case wire.AgentEventMessageEnd:
		t.applyMessageEnd(event.Message)
	case wire.AgentEventToolExecStart:
		t.startTool(event)
	case wire.AgentEventToolExecUpdate:
		t.updateTool(event)
	case wire.AgentEventToolExecEnd:
		t.endTool(event)
	case wire.AgentEventCompactionStart:
		t.closeRole(RoleAnswer, "")
		t.Append(Block{Role: RoleNotice, Text: "Compacting  " + reasonOf(event.Reason), Open: true})
	case wire.AgentEventCompactionEnd:
		line := compactionLine(event)
		if i := t.openIndex(); i >= 0 && t.blocks[i].Role == RoleNotice {
			t.Update(i, func(block *Block) {
				block.Text = line
				block.Open = false
			})
			return
		}
		t.Append(Block{Role: RoleNotice, Text: line})
	case wire.AgentEventEnd:
		// Nothing should still be in flight. A turn cut short by a cancellation
		// never sends the result, so an open call is closed as interrupted
		// rather than left spinning forever.
		t.Finish()
	}
}

// applyAssistant handles the streamed half of a message: text while it is
// still arriving, and the reasoning around it.
func (t *Transcript) applyAssistant(assistant *wire.AssistantEvent) {
	if assistant == nil {
		return
	}
	switch assistant.Type {
	case wire.EventTextDelta:
		if assistant.Delta == "" {
			return
		}
		t.closeRole(RoleThinking, "Thought for")
		i := t.openRole(RoleAnswer)
		t.Update(i, func(block *Block) { block.Text += assistant.Delta })
		t.streamed = true
		t.answer = i
	case wire.EventTextEnd:
		t.closeRole(RoleAnswer, "")
	case wire.EventThinkingStart:
		t.openRole(RoleThinking)
	case wire.EventThinkingDelta:
		if assistant.Delta == "" {
			return
		}
		i := t.openRole(RoleThinking)
		t.Update(i, func(block *Block) { block.Text += assistant.Delta })
	case wire.EventThinkingEnd:
		t.closeRole(RoleThinking, "Thought for")
	case wire.EventError:
		t.closeRole(RoleThinking, "Thought for")
		t.closeRole(RoleAnswer, "")
		text := assistant.Message.ErrorMessage
		if text == "" {
			text = "the provider reported an error"
		}
		t.Append(Block{Role: RoleError, Text: text})
	case wire.EventDone:
		t.closeRole(RoleAnswer, "")
		t.closeRole(RoleThinking, "Thought for")
	}
}

// applyMessageEnd closes the message. A provider that streamed nothing still
// has the answer on the message itself, and dropping it would leave a prompt
// with nothing under it — but a provider that did stream it has already put the
// same text on screen, so it must not be appended a second time.
func (t *Transcript) applyMessageEnd(message *wire.Message) {
	if message == nil || message.Role != wire.RoleAssistant {
		return
	}
	text := message.Text()
	switch {
	case t.streamed:
		if t.blocks[t.answer].Text == "" {
			t.Update(t.answer, func(block *Block) { block.Text = text })
		}
		t.Close(t.answer, "")
	case text != "":
		t.Close(t.Append(Block{Role: RoleAnswer, Text: text}), "")
	}
	t.streamed = false
	t.answer = 0
	// A message the reader stopped is not a failure. The provider stamps
	// "…: request aborted" into ErrorMessage, and painting that in red under a
	// conversation someone chose to leave reads as an outage they caused.
	aborted := message.StopReason == wire.StopAborted
	if !aborted && (message.StopReason == wire.StopError || message.ErrorMessage != "") {
		text := message.ErrorMessage
		if text == "" {
			text = "the provider returned an error"
		}
		t.Append(Block{Role: RoleError, Text: text})
	}
}

func (t *Transcript) startTool(event wire.AgentEvent) {
	t.closeRole(RoleAnswer, "")
	t.closeRole(RoleThinking, "Thought for")
	if i := t.openIndex(); i >= 0 && t.blocks[i].Role == RoleTool {
		// A second call started while the first is still running. Its line is
		// already on screen, so it is closed as running rather than dropped.
		t.Update(i, func(block *Block) {
			block.Open = false
			block.Progress = ""
			block.Note = joinNote(block.Note, "running")
		})
	}
	t.Append(Block{
		Role: RoleTool,
		Name: event.ToolName,
		Text: compactJSON(event.Args, 120),
		ID:   event.ToolCallID,
		Open: true,
	})
}

// progressEvery is how often a running call may rewrite its tail. A server
// picks its own reporting rate, and some pick one that would repaint faster
// than the eye can read: a quarter second is fast enough to look live and slow
// enough that the number is legible between paints.
const progressEvery = 250 * time.Millisecond

// updateTool moves a running call's progress onto its line. The tail is
// replaced, never appended: the reader needs to know the call is alive, and a
// hundred notifications for one call would otherwise push the answer it was
// made for off the screen. A call whose start was never seen has no line to
// write on — a resumed session replays messages, not events — so the update is
// dropped rather than inventing a block.
func (t *Transcript) updateTool(event wire.AgentEvent) {
	i := t.toolIndex(event.ToolCallID)
	if i < 0 {
		return
	}
	text := shortProgress(strings.Join(resultTexts(event.PartialResult), " "))
	if text == "" {
		return
	}
	block := &t.blocks[i]
	if !block.Open || text == block.Progress {
		return
	}
	now := t.clock()
	if !block.progressAt.IsZero() && now.Sub(block.progressAt) < progressEvery {
		return
	}
	block.Progress = text
	block.progressAt = now
}

func (t *Transcript) endTool(event wire.AgentEvent) {
	failed := event.IsError != nil && *event.IsError
	texts := resultTexts(event.Result)
	note := toolOutcome(texts, failed)
	detail := ""
	if failed {
		detail = resultBody(texts)
	}
	if failed && t.stopped {
		// The call did not fail: the turn was taken out from under it, and the
		// body is the kernel saying so in its own words ("Operation aborted",
		// "Command aborted"). One word in the note is the whole of what the
		// reader needs to know about a call they stopped themselves.
		note = "interrupted"
		detail = ""
	}
	if millis := event.DurationMs; millis != nil && *millis >= 1000 {
		note = joinNote(note, humanDuration(time.Duration(*millis)*time.Millisecond))
	}
	i := t.toolIndex(event.ToolCallID)
	if i < 0 {
		// The start was never seen, which happens when a resumed session's
		// transcript is replayed from messages rather than events.
		i = t.Append(Block{Role: RoleTool, Name: event.ToolName, ID: event.ToolCallID})
	}
	t.Update(i, func(block *Block) {
		block.Open = false
		block.Name = firstNonEmpty(block.Name, event.ToolName)
		// The progress tail was about a call that had not answered yet; the
		// outcome is the answer to the same question, so it takes the slot
		// rather than sitting beside a count nobody needs any more.
		block.Progress = ""
		// The line was already closed as running when a parallel call started,
		// so that marker is replaced by the real outcome rather than kept next
		// to it.
		block.Note = joinNote(withoutNote(block.Note, "running"), note)
		block.Detail = detail
	})
}

// openRole returns the index of the last block if it has the given role, and
// otherwise appends a new one.
func (t *Transcript) openRole(role Role) int {
	if i := t.openIndex(); i >= 0 && t.blocks[i].Role == role {
		return i
	}
	return t.Append(Block{Role: role, Open: true})
}

func (t *Transcript) openBlockOf(role Role) (int, bool) {
	i := t.openIndex()
	if i >= 0 && t.blocks[i].Role == role {
		return i, true
	}
	return -1, false
}

// closeRole closes the last block when it has the given role and is still open.
func (t *Transcript) closeRole(role Role, label string) {
	if i, ok := t.openBlockOf(role); ok {
		t.Close(i, label)
	}
}

func joinNote(note, addition string) string {
	switch {
	case note == "":
		return addition
	case addition == "":
		return note
	}
	return note + " · " + addition
}

// withoutNote removes one part of a joined note.
func withoutNote(note, part string) string {
	if note == "" {
		return ""
	}
	parts := strings.Split(note, " · ")
	out := parts[:0]
	for _, value := range parts {
		if value != part {
			out = append(out, value)
		}
	}
	return strings.Join(out, " · ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

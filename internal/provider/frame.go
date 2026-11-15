package provider

import (
	"encoding/json"
	"fmt"

	"pi-go/internal/wire"
)

// Frame assembles a streamed assistant message. Adapters translate their wire
// format into Start/Delta/End calls; the frame owns block indices, partial
// snapshots, and the final message. This is the Go equivalent of pi's
// assistant-message-frame.
type Frame struct {
	msg  wire.Message
	args map[int][]byte
}

// NewFrame starts an assistant message for the given model.
func NewFrame(model wire.Model) *Frame {
	return &Frame{
		msg: wire.Message{
			Role:       wire.RoleAssistant,
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			Timestamp:  wire.Now(),
			StopReason: wire.StopPending,
		},
		args: map[int][]byte{},
	}
}

// Message returns the live message. Call Clone before retaining it.
func (f *Frame) Message() wire.Message { return f.msg }

// Partial returns a snapshot for an event.
func (f *Frame) Partial() wire.Message { return f.msg.Clone() }

// Start emits the opening event.
func (f *Frame) Start() wire.AssistantEvent {
	partial := f.Partial()
	return wire.AssistantEvent{Type: wire.EventStart, Partial: &partial}
}

// TextStart opens a text block at contentIndex.
func (f *Frame) TextStart(index int) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentText
	block.Text = ""
	return f.update(wire.EventTextStart, index, "")
}

// TextDelta appends text to the block at contentIndex.
func (f *Frame) TextDelta(index int, delta string) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentText
	block.Text += delta
	return f.update(wire.EventTextDelta, index, delta)
}

// TextEnd closes the text block and reports its full content.
func (f *Frame) TextEnd(index int) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentText
	return f.contentEnd(wire.EventTextEnd, index, block.Text)
}

// ThinkingStart opens a thinking block.
func (f *Frame) ThinkingStart(index int) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentThinking
	block.Thinking = ""
	return f.update(wire.EventThinkingStart, index, "")
}

// ThinkingDelta appends reasoning text.
func (f *Frame) ThinkingDelta(index int, delta string) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentThinking
	block.Thinking += delta
	return f.update(wire.EventThinkingDelta, index, delta)
}

// ThinkingEnd closes the thinking block.
func (f *Frame) ThinkingEnd(index int) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentThinking
	return f.contentEnd(wire.EventThinkingEnd, index, block.Thinking)
}

// ThinkingSignature records which provider field produced a thinking block, so
// a later request can replay the text under the same name. The first signature
// wins; vendors do not rename the field mid-stream.
func (f *Frame) ThinkingSignature(index int, signature string) {
	block := f.msg.Content.Ensure(index)
	if block.Type != wire.ContentThinking || signature == "" || block.ThinkingSignature != "" {
		return
	}
	block.ThinkingSignature = signature
}

// AppendThinkingSignature appends a fragment to a thinking block's signature.
// Anthropic streams the signature of a reasoning block as fragments after the
// reasoning text, so the value is assembled across events. For chat completions
// the same field holds the name of the vendor field that carried the reasoning,
// which is why the two cannot share a setter.
func (f *Frame) AppendThinkingSignature(index int, delta string) {
	block := f.msg.Content.Ensure(index)
	if block.Type != wire.ContentThinking || delta == "" {
		return
	}
	block.ThinkingSignature += delta
}

// TextSignature records an opaque token a provider attached to a text block.
// Gemini puts a thought signature on a part whether or not that part carries
// reasoning, and a later request has to echo it back verbatim; the field is
// shared with pi's transcripts for the same reason. The first non-empty value
// wins, because the signature describes the whole block and later fragments
// repeat rather than extend it.
func (f *Frame) TextSignature(index int, signature string) {
	block := f.msg.Content.Ensure(index)
	if block.Type != wire.ContentText || signature == "" || block.TextSignature != "" {
		return
	}
	block.TextSignature = signature
}

// ThoughtSignature does the same for a tool call: a call that arrived with a
// signature must be replayed with it, or a reasoning model refuses the
// transcript.
func (f *Frame) ThoughtSignature(index int, signature string) {
	block := f.msg.Content.Ensure(index)
	if block.Type != wire.ContentToolCall || signature == "" || block.ThoughtSignature != "" {
		return
	}
	block.ThoughtSignature = signature
}

// RedactedThinkingStart opens a thinking block whose reasoning text the
// provider withheld. data is the opaque payload to send back on replay.
func (f *Frame) RedactedThinkingStart(index int, data string) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentThinking
	block.Thinking = redactedThinkingText
	block.ThinkingSignature = data
	redacted := true
	block.Redacted = &redacted
	return f.update(wire.EventThinkingStart, index, "")
}

// redactedThinkingText is what the model and the UI see for a withheld block.
// It matches pi, which needs some text because the block still occupies a
// content index.
const redactedThinkingText = "[Reasoning redacted]"

// ToolCallStart opens a tool call block.
func (f *Frame) ToolCallStart(index int, id, name string) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentToolCall
	block.ID = id
	block.Name = name
	block.Arguments = json.RawMessage("{}")
	f.args[index] = nil
	return f.update(wire.EventToolCallStart, index, "")
}

// ToolCallIdentify fills the id and name of an already started tool call.
// Providers disagree on when these arrive: some send them on the first
// fragment, others only on a later one. Missing values are left untouched so a
// late fragment cannot erase an earlier one. No event is emitted, because the
// call was already announced by ToolCallStart; the next delta carries the
// updated partial.
func (f *Frame) ToolCallIdentify(index int, id, name string) {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentToolCall
	if id != "" {
		block.ID = id
	}
	if name != "" {
		block.Name = name
	}
}

// ToolCallDelta appends a fragment of the JSON arguments. A fragment that is
// not yet valid JSON stays buffered; the event carries {} until it is.
func (f *Frame) ToolCallDelta(index int, delta string) wire.AssistantEvent {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentToolCall
	buf := append(f.args[index], delta...)
	f.args[index] = buf
	if json.Valid(buf) {
		block.Arguments = json.RawMessage(append([]byte(nil), buf...))
	}
	return f.update(wire.EventToolCallDelta, index, delta)
}

// ToolCallEnd validates the accumulated arguments and returns the finished
// call. It fails when the model never produced a valid JSON object.
func (f *Frame) ToolCallEnd(index int) (wire.AssistantEvent, error) {
	block := f.msg.Content.Ensure(index)
	block.Type = wire.ContentToolCall
	buf := f.args[index]
	if len(buf) == 0 {
		buf = []byte("{}")
	}
	if !json.Valid(buf) {
		return wire.AssistantEvent{}, fmt.Errorf("tool call %q arguments are not valid JSON: %s", block.Name, string(buf))
	}
	block.Arguments = json.RawMessage(append([]byte(nil), buf...))
	call := *block
	partial := f.Partial()
	return wire.AssistantEvent{Type: wire.EventToolCallEnd, ContentIndex: wire.Ptr(index), ToolCall: &call, Partial: &partial}, nil
}

// SetUsage records token and cost accounting for the final message.
func (f *Frame) SetUsage(usage wire.Usage) { f.msg.Usage = &usage }

// Done terminates the message with a successful stop reason.
func (f *Frame) Done(reason wire.StopReason) wire.AssistantEvent {
	f.msg.StopReason = reason
	message := f.msg.Clone()
	return wire.AssistantEvent{Type: wire.EventDone, Reason: string(reason), Message: &message}
}

// Error terminates the message with a failure.
//
// The message also carries a structured code when the text names a failure the
// caller can act on. Stamping it here rather than in each adapter is what keeps
// the four dialects consistent: every failure path an adapter has — a transport
// error, a rejected request, a malformed stream — funnels through this call
// eventually, so none of them can forget to classify.
func (f *Frame) Error(reason wire.StopReason, message string) wire.AssistantEvent {
	f.msg.StopReason = reason
	f.msg.ErrorMessage = message
	if reason == wire.StopError {
		f.msg.ErrorCode = ClassifyText(message)
	}
	final := f.msg.Clone()
	return wire.AssistantEvent{Type: wire.EventError, Reason: string(reason), Error: &final}
}

func (f *Frame) update(eventType string, index int, delta string) wire.AssistantEvent {
	partial := f.Partial()
	return wire.AssistantEvent{
		Type:         eventType,
		ContentIndex: wire.Ptr(index),
		Delta:        delta,
		Partial:      &partial,
	}
}

func (f *Frame) contentEnd(eventType string, index int, content string) wire.AssistantEvent {
	partial := f.Partial()
	return wire.AssistantEvent{
		Type:         eventType,
		ContentIndex: wire.Ptr(index),
		Content:      content,
		Partial:      &partial,
	}
}

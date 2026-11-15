package rpc

import (
	"fmt"

	"pi-go/internal/wire"
)

// jsonAssistantEvent is an assistant streaming event without the cumulative
// partial message.
//
// pi strips `partial` before writing the event because it repeats the whole
// message on every delta and dominates the protocol volume. What survives is
// the per-event data plus, for a tool call start, the id and name, which are
// constant-size and let a client label the argument stream it is about to
// receive.
type jsonAssistantEvent struct {
	Type         string        `json:"type"`
	ContentIndex *int          `json:"contentIndex,omitempty"`
	Delta        string        `json:"delta,omitempty"`
	Content      string        `json:"content,omitempty"`
	ToolCall     *wire.Content `json:"toolCall,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	Message      *wire.Message `json:"message,omitempty"`
	Error        *wire.Message `json:"error,omitempty"`

	ID       string `json:"id,omitempty"`
	ToolName string `json:"toolName,omitempty"`
}

// jsonMessageUpdate is the JSON form of a message_update event.
type jsonMessageUpdate struct {
	Type                  string             `json:"type"`
	Usage                 wire.Usage         `json:"usage"`
	AssistantMessageEvent jsonAssistantEvent `json:"assistantMessageEvent"`
}

// encodeEvent converts one kernel event into the value JSONL carries.
//
// Every event is already shaped like pi's except message_update, which carries
// a cumulative snapshot that the JSON protocols replace with the usage and the
// delta-level event. The stream level events are the expensive ones, so the
// conversion is worth doing once, here, instead of in each transport.
func encodeEvent(event wire.AgentEvent) (any, error) {
	if event.Type != wire.AgentEventMessageUpdate {
		return event, nil
	}
	if event.Message == nil || event.Message.Role != wire.RoleAssistant {
		return nil, fmt.Errorf("rpc: message_update is not an assistant message")
	}
	if event.AssistantEvent == nil {
		return nil, fmt.Errorf("rpc: message_update has no assistant event")
	}

	// Usage is present on every JSON message_update, zeroed while the provider
	// has not reported it yet, because the field is required rather than
	// optional. Reporting "absent" for a streaming snapshot would make clients
	// branch on a case pi never produces.
	usage := wire.Usage{}
	if event.Message.Usage != nil {
		usage = *event.Message.Usage
	}

	assistant, err := jsonAssistant(*event.AssistantEvent, event.Message)
	if err != nil {
		return nil, err
	}
	return jsonMessageUpdate{
		Type:                  wire.AgentEventMessageUpdate,
		Usage:                 usage,
		AssistantMessageEvent: assistant,
	}, nil
}

// jsonAssistant converts one assistant streaming event. message is the
// accumulated assistant message the event belongs to.
func jsonAssistant(event wire.AssistantEvent, message *wire.Message) (jsonAssistantEvent, error) {
	out := jsonAssistantEvent{
		Type:         event.Type,
		ContentIndex: event.ContentIndex,
		Delta:        event.Delta,
		Content:      event.Content,
		ToolCall:     event.ToolCall,
		Reason:       event.Reason,
		Message:      event.Message,
		Error:        event.Error,
	}
	if event.Type != "toolcall_start" {
		return out, nil
	}

	// The id and name of a tool call arrive inside the content block, not on
	// the event, so a client that only reads events would see an unlabelled
	// argument stream.
	if event.ContentIndex == nil {
		return jsonAssistantEvent{}, fmt.Errorf("rpc: toolcall_start has no content index")
	}
	index := *event.ContentIndex
	partial := message
	if event.Partial != nil {
		partial = event.Partial
	}
	block := partial.Content.At(index)
	if block == nil || block.Type != wire.ContentToolCall {
		return jsonAssistantEvent{}, fmt.Errorf("rpc: toolcall_start content at index %d is not a tool call", index)
	}
	out.ID = block.ID
	out.ToolName = block.Name
	return out, nil
}

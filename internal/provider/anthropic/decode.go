package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/sse"
	"pi-go/internal/wire"
)

// streamEvent is one `data:` payload. Every message-level field is a pointer
// because absence and zero mean different things: a message_delta may omit
// input_tokens, and stop_reason is null until the model is done.
type streamEvent struct {
	Type         string        `json:"type"`
	Index        *int          `json:"index"`
	Message      *startMessage `json:"message"`
	ContentBlock *contentBlock `json:"content_block"`
	Delta        *delta        `json:"delta"`
	Usage        *usage        `json:"usage"`
	Error        *apiError     `json:"error"`
}

type startMessage struct {
	Usage usage `json:"usage"`
}

type contentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Data      string `json:"data"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

type delta struct {
	Type        string      `json:"type"`
	Text        string      `json:"text"`
	Thinking    string      `json:"thinking"`
	PartialJSON string      `json:"partial_json"`
	Signature   string      `json:"signature"`
	StopReason  *string     `json:"stop_reason"`
	StopDetails *stopDetail `json:"stop_details"`
}

type stopDetail struct {
	Explanation string `json:"explanation"`
}

type usage struct {
	InputTokens              *int          `json:"input_tokens"`
	OutputTokens             *int          `json:"output_tokens"`
	CacheReadInputTokens     *int          `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int          `json:"cache_creation_input_tokens"`
	CacheCreation            *cacheDetail  `json:"cache_creation"`
	OutputTokensDetails      *outputDetail `json:"output_tokens_details"`
}

type cacheDetail struct {
	Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens"`
}

type outputDetail struct {
	ThinkingTokens *int `json:"thinking_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	if e.Type == "" {
		return "anthropic: stream error: " + e.Message
	}
	return fmt.Sprintf("anthropic: stream error (%s): %s", e.Type, e.Message)
}

// mapStopReason mirrors pi. An unknown reason is a failure rather than a silent
// stop, because a new value may mean content the kernel does not understand.
func mapStopReason(reason string, details *stopDetail) (wire.StopReason, string) {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
		return wire.StopStop, ""
	case "max_tokens":
		return wire.StopLength, ""
	case "tool_use":
		return wire.StopToolUse, ""
	case "refusal":
		explanation := ""
		if details != nil {
			explanation = details.Explanation
		}
		if explanation == "" {
			explanation = "The model refused to complete the request"
		}
		return wire.StopError, explanation
	case "sensitive":
		return wire.StopError, "Provider stopped with: sensitive"
	default:
		return wire.StopError, "Unhandled stop reason: " + reason
	}
}

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockToolCall
)

// decoder turns message events into frame calls. It owns the mapping from the
// API's content block indices to content indices, which the frame deliberately
// does not guess: a provider that skips indices would otherwise leave holes in
// the content array.
type decoder struct {
	frame  *provider.Frame
	at     map[int]int
	kinds  []blockKind
	closed map[int]bool

	input      int
	output     int
	cacheRead  int
	cacheWrite int
	cache1h    *int
	reasoning  *int

	stopSeen    bool
	stopReason  wire.StopReason
	stopMessage string
}

func newDecoder(model wire.Model) *decoder {
	return &decoder{
		frame:  provider.NewFrame(model),
		at:     map[int]int{},
		closed: map[int]bool{},
	}
}

// feed decodes one event. The bool reports that the message is complete.
func (d *decoder) feed(name, data string) ([]wire.AssistantEvent, bool, error) {
	if strings.TrimSpace(data) == "" {
		return nil, false, nil
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, false, fmt.Errorf("anthropic: decode event: %w", err)
	}
	if event.Type == "" {
		// Some proxies drop the data type and keep the event name.
		event.Type = name
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			d.applyUsage(event.Message.Usage)
		}
		return nil, false, nil

	case "content_block_start":
		events, err := d.startBlock(event)
		return events, false, err

	case "content_block_delta":
		events, err := d.delta(event)
		return events, false, err

	case "content_block_stop":
		events, err := d.stopBlock(event)
		return events, false, err

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != nil && *event.Delta.StopReason != "" {
			d.stopSeen = true
			d.stopReason, d.stopMessage = mapStopReason(*event.Delta.StopReason, event.Delta.StopDetails)
		}
		if event.Usage != nil {
			d.applyUsage(*event.Usage)
		}
		return nil, false, nil

	case "message_stop":
		events, err := d.finish()
		return events, true, err

	case "ping":
		return nil, false, nil

	case "error":
		if event.Error != nil {
			return nil, false, event.Error
		}
		return nil, false, errors.New("anthropic: stream error without a payload")

	default:
		// Unknown event types are ignored: the API adds them over time and a
		// forward-compatible client must not fail on a name it does not know.
		return nil, false, nil
	}
}

func (d *decoder) startBlock(event streamEvent) ([]wire.AssistantEvent, error) {
	if event.Index == nil || event.ContentBlock == nil {
		return nil, nil
	}
	block := *event.ContentBlock
	index := len(d.kinds)
	switch block.Type {
	case "text":
		d.kinds = append(d.kinds, blockText)
		d.at[*event.Index] = index
		events := []wire.AssistantEvent{d.frame.TextStart(index)}
		if block.Text != "" {
			// A block may open with text already in it. The API sends an empty
			// string, but a gateway that folds the first delta into the start
			// event would otherwise lose it, and every reader here counts
			// deltas rather than diffing partials.
			events = append(events, d.frame.TextDelta(index, block.Text))
		}
		return events, nil
	case "thinking":
		d.kinds = append(d.kinds, blockThinking)
		d.at[*event.Index] = index
		events := []wire.AssistantEvent{d.frame.ThinkingStart(index)}
		if block.Thinking != "" {
			events = append(events, d.frame.ThinkingDelta(index, block.Thinking))
		}
		if block.Signature != "" {
			d.frame.AppendThinkingSignature(index, block.Signature)
		}
		return events, nil
	case "redacted_thinking":
		d.kinds = append(d.kinds, blockThinking)
		d.at[*event.Index] = index
		return []wire.AssistantEvent{d.frame.RedactedThinkingStart(index, block.Data)}, nil
	case "tool_use":
		d.kinds = append(d.kinds, blockToolCall)
		d.at[*event.Index] = index
		return []wire.AssistantEvent{d.frame.ToolCallStart(index, block.ID, block.Name)}, nil
	default:
		// Includes "fallback", the server-side model fallback marker, which
		// carries no content of its own.
		return nil, nil
	}
}

func (d *decoder) delta(event streamEvent) ([]wire.AssistantEvent, error) {
	if event.Index == nil || event.Delta == nil {
		return nil, nil
	}
	index, ok := d.at[*event.Index]
	if !ok || d.closed[index] {
		return nil, nil
	}
	switch event.Delta.Type {
	case "text_delta":
		if event.Delta.Text == "" {
			return nil, nil
		}
		return []wire.AssistantEvent{d.frame.TextDelta(index, event.Delta.Text)}, nil
	case "thinking_delta":
		if event.Delta.Thinking == "" {
			return nil, nil
		}
		return []wire.AssistantEvent{d.frame.ThinkingDelta(index, event.Delta.Thinking)}, nil
	case "input_json_delta":
		if event.Delta.PartialJSON == "" {
			return nil, nil
		}
		return []wire.AssistantEvent{d.frame.ToolCallDelta(index, event.Delta.PartialJSON)}, nil
	case "signature_delta":
		d.frame.AppendThinkingSignature(index, event.Delta.Signature)
		return nil, nil
	default:
		return nil, nil
	}
}

func (d *decoder) stopBlock(event streamEvent) ([]wire.AssistantEvent, error) {
	if event.Index == nil {
		return nil, nil
	}
	index, ok := d.at[*event.Index]
	if !ok || d.closed[index] {
		return nil, nil
	}
	d.closed[index] = true
	switch d.kinds[index] {
	case blockText:
		return []wire.AssistantEvent{d.frame.TextEnd(index)}, nil
	case blockThinking:
		return []wire.AssistantEvent{d.frame.ThinkingEnd(index)}, nil
	default:
		event, err := d.endToolCall(index)
		if err != nil {
			return nil, err
		}
		return []wire.AssistantEvent{event}, nil
	}
}

func (d *decoder) endToolCall(index int) (wire.AssistantEvent, error) {
	message := d.frame.Message()
	block := message.Content.At(index)
	if block == nil || block.ID == "" {
		return wire.AssistantEvent{}, fmt.Errorf("anthropic: tool call %d arrived without an id", index)
	}
	event, err := d.frame.ToolCallEnd(index)
	if err != nil {
		return wire.AssistantEvent{}, fmt.Errorf("anthropic: %w", err)
	}
	return event, nil
}

// applyUsage records a usage snapshot. Components are updated only when the
// event carries them: message_start reports input and cache counts, while
// message_delta reports the final output count, and a value missing from one of
// them must not overwrite what the other established.
func (d *decoder) applyUsage(raw usage) {
	if raw.InputTokens != nil {
		d.input = *raw.InputTokens
	}
	if raw.OutputTokens != nil {
		d.output = *raw.OutputTokens
	}
	if raw.CacheReadInputTokens != nil {
		d.cacheRead = *raw.CacheReadInputTokens
	}
	if raw.CacheCreationInputTokens != nil {
		d.cacheWrite = *raw.CacheCreationInputTokens
	}
	if raw.CacheCreation != nil && raw.CacheCreation.Ephemeral1hInputTokens != nil {
		value := *raw.CacheCreation.Ephemeral1hInputTokens
		d.cache1h = &value
	}
	if raw.OutputTokensDetails != nil && raw.OutputTokensDetails.ThinkingTokens != nil {
		value := *raw.OutputTokensDetails.ThinkingTokens
		d.reasoning = &value
	}
	// The API does not report a total; pi computes it from the components
	// without subtracting the cache counts, because input_tokens already
	// excludes them.
	d.frame.SetUsage(wire.Usage{
		Input:        d.input,
		Output:       d.output,
		CacheRead:    d.cacheRead,
		CacheWrite:   d.cacheWrite,
		CacheWrite1h: d.cache1h,
		Reasoning:    d.reasoning,
		TotalTokens:  d.input + d.output + d.cacheRead + d.cacheWrite,
	})
}

// finish closes any block the stream did not close and emits the terminal
// event. A message that ends without a stop reason is a truncated stream, not a
// successful stop.
func (d *decoder) finish() ([]wire.AssistantEvent, error) {
	events := make([]wire.AssistantEvent, 0, len(d.kinds)+1)
	for index, kind := range d.kinds {
		if d.closed[index] {
			continue
		}
		d.closed[index] = true
		switch kind {
		case blockText:
			events = append(events, d.frame.TextEnd(index))
		case blockThinking:
			events = append(events, d.frame.ThinkingEnd(index))
		case blockToolCall:
			event, err := d.endToolCall(index)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
	}

	if !d.stopSeen {
		return nil, errors.New("Anthropic stream ended without a stop reason")
	}
	if d.stopReason == wire.StopError {
		return append(events, d.frame.Error(wire.StopError, d.stopMessage)), nil
	}
	return append(events, d.frame.Done(d.stopReason)), nil
}

// stream reads one HTTP response and exposes it as assistant events.
type stream struct {
	ctx       context.Context
	dec       *decoder
	reader    *sse.Reader
	body      io.ReadCloser
	queue     []wire.AssistantEvent
	pending   error
	terminal  bool
	finalized bool
}

func newStream(ctx context.Context, model wire.Model, body io.ReadCloser) *stream {
	s := &stream{
		ctx:    ctx,
		dec:    newDecoder(model),
		reader: sse.NewReader(body),
		body:   body,
	}
	s.queue = append(s.queue, s.dec.frame.Start())
	return s
}

func (s *stream) Recv() (wire.AssistantEvent, error) {
	for {
		if len(s.queue) > 0 {
			event := s.queue[0]
			s.queue = s.queue[1:]
			return event, nil
		}
		if s.terminal {
			return wire.AssistantEvent{}, io.EOF
		}
		if s.pending != nil {
			err := s.pending
			s.pending = nil
			s.fail(err)
			continue
		}

		event, err := s.reader.Next()
		switch {
		case errors.Is(err, io.EOF):
			s.complete()
		case err != nil:
			s.pending = err
		default:
			events, done, feedErr := s.dec.feed(event.Name, event.Data)
			if feedErr != nil {
				s.pending = feedErr
				continue
			}
			s.queue = append(s.queue, events...)
			if done {
				s.finalized = true
				s.terminal = true
			}
		}
	}
}

// complete finalizes a stream that ended without message_stop.
func (s *stream) complete() {
	if s.finalized {
		s.terminal = true
		return
	}
	s.finalized = true
	events, err := s.dec.finish()
	if err != nil {
		s.fail(err)
		return
	}
	s.queue = append(s.queue, events...)
	s.terminal = true
}

func (s *stream) fail(err error) {
	reason := wire.StopError
	message := err.Error()
	if s.ctx.Err() != nil {
		reason = wire.StopAborted
		message = "anthropic: request aborted"
	}
	s.queue = append(s.queue, s.dec.frame.Error(reason, message))
	s.finalized = true
	s.terminal = true
}

// Close releases the response body. Recv is not safe for concurrent use, which
// matches provider.Stream.
func (s *stream) Close() error { return s.body.Close() }

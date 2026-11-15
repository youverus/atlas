package responses

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

// streamEvent is one `data:` payload. The type inside the JSON is authoritative
// even though these frames also carry an `event:` name: a proxy that rewrites
// names must not desynchronise the decoder.
type streamEvent struct {
	Type        string          `json:"type"`
	OutputIndex *int            `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Response    *streamResponse `json:"response"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
}

// item is the subset of an output item the decoder reads. The raw bytes are what
// gets stored: a reasoning item is a blob the server issued, and re-encoding it
// through these fields would drop the rest of it.
type item struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// text is the finished text of a message item: output text and refusals share
// the block, so both are read in order.
func (i item) text() string {
	var out strings.Builder
	for _, part := range i.Content {
		if part.Type == "output_text" {
			out.WriteString(part.Text)
			continue
		}
		out.WriteString(part.Refusal)
	}
	return out.String()
}

// thinkingText is the finished reasoning text. A summary is what the API usually
// returns; a raw reasoning payload arrives in content instead.
func (i item) thinkingText() string {
	if len(i.Summary) > 0 {
		parts := make([]string, 0, len(i.Summary))
		for _, part := range i.Summary {
			parts = append(parts, part.Text)
		}
		return strings.Join(parts, "\n\n")
	}
	if len(i.Content) > 0 {
		parts := make([]string, 0, len(i.Content))
		for _, part := range i.Content {
			parts = append(parts, part.Text)
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

type streamResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Usage *streamUsage `json:"usage"`
}

// streamUsage reports the cache counters inside input_tokens, which is the
// opposite of the chat completions accounting: here they must be subtracted,
// there they are already separate.
type streamUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// thinkingPartSeparator stands between the parts of a reasoning summary.
const thinkingPartSeparator = "\n\n"

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockToolCall
)

// decoder turns events into frame calls. slots maps the server's output_index to
// a content index: the server numbers items, including ones this adapter does not
// render such as a web search call, while the message numbers blocks, so the two
// must not be assumed equal.
type decoder struct {
	frame *provider.Frame
	slots map[int]int
	kinds []blockKind
	// closed marks blocks already finished, so an item done event and the final
	// sweep cannot both close one.
	closed []bool

	// text, thinking and args hold what the reader has been shown for each
	// content index. The frame keeps the same values; these copies exist so a
	// cumulative event can be turned into the missing tail.
	text     map[int]*strings.Builder
	thinking map[int]*strings.Builder
	args     map[int]*strings.Builder

	// separator marks a reasoning block whose summary part just ended, so the
	// next part opens with a blank line.
	separator map[int]bool

	stopReason  wire.StopReason
	stopMessage string
}

func newDecoder(model wire.Model) *decoder {
	return &decoder{
		frame:     provider.NewFrame(model),
		slots:     map[int]int{},
		text:      map[int]*strings.Builder{},
		thinking:  map[int]*strings.Builder{},
		args:      map[int]*strings.Builder{},
		separator: map[int]bool{},
	}
}

// feed decodes one event. The bool reports that the message is terminal.
func (d *decoder) feed(data string) ([]wire.AssistantEvent, bool, error) {
	if strings.TrimSpace(data) == "" {
		return nil, false, nil
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, false, fmt.Errorf("responses: decode event: %w", err)
	}

	switch event.Type {
	case "response.output_item.added":
		events, err := d.addItem(event)
		return events, false, err

	case "response.output_text.delta", "response.refusal.delta":
		index, ok := d.slotOf(event.OutputIndex, blockText)
		if !ok {
			return nil, false, nil
		}
		return d.pushText(index, event.Delta), false, nil

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		index, ok := d.slotOf(event.OutputIndex, blockThinking)
		if !ok {
			return nil, false, nil
		}
		return d.pushThinking(index, event.Delta), false, nil

	case "response.reasoning_summary_part.done":
		index, ok := d.slotOf(event.OutputIndex, blockThinking)
		if !ok {
			return nil, false, nil
		}
		d.separator[index] = true
		return nil, false, nil

	case "response.function_call_arguments.delta":
		index, ok := d.slotOf(event.OutputIndex, blockToolCall)
		if !ok {
			return nil, false, nil
		}
		return d.pushArguments(index, event.Delta), false, nil

	case "response.function_call_arguments.done":
		index, ok := d.slotOf(event.OutputIndex, blockToolCall)
		if !ok {
			return nil, false, nil
		}
		return d.reconcileArguments(index, event.Arguments), false, nil

	case "response.output_item.done":
		events, err := d.doneItem(event)
		return events, false, err

	case "response.completed", "response.incomplete":
		events, err := d.complete(event)
		return events, true, err

	case "response.failed":
		events, err := d.failed(event)
		return events, true, err

	case "error":
		detail := event.Message
		if event.Code != "" {
			detail = fmt.Sprintf("%s: %s", event.Code, event.Message)
		}
		return nil, false, errors.New("responses: stream error: " + detail)

	default:
		// response.created, in_progress, content_part events and anything the
		// API adds later carry nothing this kernel renders.
		return nil, false, nil
	}
}

// addItem opens a block for an output item that started.
func (d *decoder) addItem(event streamEvent) ([]wire.AssistantEvent, error) {
	if event.OutputIndex == nil {
		return nil, nil
	}
	if _, ok := d.slots[*event.OutputIndex]; ok {
		return nil, nil
	}
	parsed, err := decodeItem(event.Item)
	if err != nil {
		return nil, err
	}
	_, events, err := d.open(*event.OutputIndex, parsed)
	return events, err
}

// doneItem finishes the block an item belongs to. Some servers skip the added
// event, so a missing block is opened here rather than dropped.
func (d *decoder) doneItem(event streamEvent) ([]wire.AssistantEvent, error) {
	if event.OutputIndex == nil || len(event.Item) == 0 {
		return nil, nil
	}
	parsed, err := decodeItem(event.Item)
	if err != nil {
		return nil, err
	}
	index, events, err := d.open(*event.OutputIndex, parsed)
	if err != nil || index < 0 {
		return nil, err
	}
	if d.closed[index] {
		return events, nil
	}

	switch d.kinds[index] {
	case blockThinking:
		// The item as issued is the signature: it is the only form the server
		// accepts back, and it carries the encrypted reasoning text.
		d.frame.ThinkingSignature(index, string(event.Item))
		return append(events, d.finishThinking(index, parsed.thinkingText())...), nil
	case blockText:
		return append(events, d.finishText(index, parsed.text())...), nil
	default:
		events = append(events, d.reconcileArguments(index, parsed.Arguments)...)
		d.closed[index] = true
		call, err := d.endToolCall(index)
		if err != nil {
			return nil, err
		}
		return append(events, call), nil
	}
}

// open returns the content index for an output index, creating the block on first
// sight. -1 means the item is one this adapter does not render.
func (d *decoder) open(outputIndex int, parsed item) (int, []wire.AssistantEvent, error) {
	if index, ok := d.slots[outputIndex]; ok {
		return index, nil, nil
	}
	index := len(d.kinds)
	switch parsed.Type {
	case "reasoning":
		d.kinds = append(d.kinds, blockThinking)
		d.closed = append(d.closed, false)
		d.thinking[index] = &strings.Builder{}
		d.slots[outputIndex] = index
		return index, []wire.AssistantEvent{d.frame.ThinkingStart(index)}, nil
	case "message":
		d.kinds = append(d.kinds, blockText)
		d.closed = append(d.closed, false)
		d.text[index] = &strings.Builder{}
		d.slots[outputIndex] = index
		return index, []wire.AssistantEvent{d.frame.TextStart(index)}, nil
	case "function_call":
		d.kinds = append(d.kinds, blockToolCall)
		d.closed = append(d.closed, false)
		d.args[index] = &strings.Builder{}
		d.slots[outputIndex] = index
		events := []wire.AssistantEvent{d.frame.ToolCallStart(index, parsed.CallID, parsed.Name)}
		return index, append(events, d.pushArguments(index, parsed.Arguments)...), nil
	default:
		return -1, nil, nil
	}
}

// slotOf resolves an output index to a content index of the expected kind.
func (d *decoder) slotOf(outputIndex *int, kind blockKind) (int, bool) {
	if outputIndex == nil {
		return 0, false
	}
	index, ok := d.slots[*outputIndex]
	if !ok || d.closed[index] || d.kinds[index] != kind {
		return 0, false
	}
	return index, true
}

func (d *decoder) pushText(index int, delta string) []wire.AssistantEvent {
	if delta == "" {
		return nil
	}
	d.text[index].WriteString(delta)
	return []wire.AssistantEvent{d.frame.TextDelta(index, delta)}
}

func (d *decoder) pushThinking(index int, delta string) []wire.AssistantEvent {
	var events []wire.AssistantEvent
	if d.separator[index] {
		// A finished summary part means another one follows, and the API sends no
		// separator between them. The separator waits for that successor: one
		// trailing the last part would leave the block ending in blank lines.
		d.separator[index] = false
		d.thinking[index].WriteString(thinkingPartSeparator)
		events = append(events, d.frame.ThinkingDelta(index, thinkingPartSeparator))
	}
	if delta == "" {
		return events
	}
	d.thinking[index].WriteString(delta)
	return append(events, d.frame.ThinkingDelta(index, delta))
}

func (d *decoder) pushArguments(index int, delta string) []wire.AssistantEvent {
	if delta == "" {
		return nil
	}
	d.args[index].WriteString(delta)
	return []wire.AssistantEvent{d.frame.ToolCallDelta(index, delta)}
}

// reconcileArguments extends a call with a restated argument string. The API
// sends the whole string on some events and only a fragment on others, so the
// difference is what the reader has not seen.
func (d *decoder) reconcileArguments(index int, final string) []wire.AssistantEvent {
	tail, ok := missingTail(d.args[index].String(), final)
	if !ok {
		return nil
	}
	return d.pushArguments(index, tail)
}

// finishText closes a text block with the text the server finished with. A
// server that streamed nothing is the reason this is not just TextEnd: the block
// would otherwise stay empty. Text the server rewrote into something that is not
// an extension of what was streamed is left as delivered, because a delta cannot
// take text back and a reader already saw it.
func (d *decoder) finishText(index int, final string) []wire.AssistantEvent {
	events := []wire.AssistantEvent{}
	if tail, ok := missingTail(d.text[index].String(), final); ok {
		events = append(events, d.pushText(index, tail)...)
	}
	d.closed[index] = true
	return append(events, d.frame.TextEnd(index))
}

func (d *decoder) finishThinking(index int, final string) []wire.AssistantEvent {
	events := []wire.AssistantEvent{}
	if tail, ok := missingTail(d.thinking[index].String(), final); ok {
		events = append(events, d.pushThinking(index, tail)...)
	}
	d.closed[index] = true
	return append(events, d.frame.ThinkingEnd(index))
}

// complete ends the message on a completed or incomplete response.
func (d *decoder) complete(event streamEvent) ([]wire.AssistantEvent, error) {
	events, err := d.closeOpen()
	if err != nil {
		return nil, err
	}
	if response := event.Response; response != nil {
		d.setUsage(response.Usage)
		d.stopReason, d.stopMessage = mapStopReason(response.Status, incompleteReason(response))
	} else {
		d.stopReason = wire.StopStop
	}
	if d.hasToolCall() && d.stopReason == wire.StopStop {
		d.stopReason = wire.StopToolUse
	}
	if d.stopReason == wire.StopError {
		return append(events, d.frame.Error(wire.StopError, d.stopMessage)), nil
	}
	return append(events, d.frame.Done(d.stopReason)), nil
}

// failed ends the message on a failed response. The API reports the reason in two
// places, so both are read before falling back to a fixed sentence.
func (d *decoder) failed(event streamEvent) ([]wire.AssistantEvent, error) {
	events, err := d.closeOpen()
	if err != nil {
		return nil, err
	}
	message := "responses: the provider reported a failed response"
	if response := event.Response; response != nil {
		d.setUsage(response.Usage)
		switch {
		case response.Error != nil:
			code := response.Error.Code
			if code == "" {
				code = "unknown"
			}
			text := response.Error.Message
			if text == "" {
				text = "no message"
			}
			message = fmt.Sprintf("responses: %s: %s", code, text)
		case incompleteReason(response) != "":
			message = "responses: incomplete: " + incompleteReason(response)
		}
	}
	d.stopReason = wire.StopError
	return append(events, d.frame.Error(wire.StopError, message)), nil
}

// closeOpen finishes every block the stream left open.
func (d *decoder) closeOpen() ([]wire.AssistantEvent, error) {
	var events []wire.AssistantEvent
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
		default:
			call, err := d.endToolCall(index)
			if err != nil {
				return nil, err
			}
			events = append(events, call)
		}
	}
	return events, nil
}

func (d *decoder) endToolCall(index int) (wire.AssistantEvent, error) {
	message := d.frame.Message()
	block := message.Content.At(index)
	if block == nil || block.ID == "" {
		return wire.AssistantEvent{}, fmt.Errorf("responses: tool call %d arrived without a call id", index)
	}
	event, err := d.frame.ToolCallEnd(index)
	if err != nil {
		return wire.AssistantEvent{}, fmt.Errorf("responses: %w", err)
	}
	return event, nil
}

func (d *decoder) hasToolCall() bool {
	for _, kind := range d.kinds {
		if kind == blockToolCall {
			return true
		}
	}
	return false
}

// setUsage records the response's accounting. Cached and written tokens are part
// of input_tokens here, so both are subtracted; the server's total is kept when
// it reports one, because it is the number it billed.
func (d *decoder) setUsage(raw *streamUsage) {
	if raw == nil {
		return
	}
	cached, written := 0, 0
	if raw.InputTokensDetails != nil {
		cached = raw.InputTokensDetails.CachedTokens
		written = raw.InputTokensDetails.CacheWriteTokens
	}
	input := raw.InputTokens - cached - written
	if input < 0 {
		input = 0
	}
	total := raw.TotalTokens
	if total == 0 {
		total = input + raw.OutputTokens + cached + written
	}
	usage := wire.Usage{
		Input:       input,
		Output:      raw.OutputTokens,
		CacheRead:   cached,
		CacheWrite:  written,
		TotalTokens: total,
	}
	if raw.OutputTokensDetails != nil && raw.OutputTokensDetails.ReasoningTokens > 0 {
		reasoning := raw.OutputTokensDetails.ReasoningTokens
		usage.Reasoning = &reasoning
	}
	d.frame.SetUsage(usage)
}

// mapStopReason mirrors pi, including its refusal to guess: a status the adapter
// does not know is a failure, not a silent stop, because a new value may mean
// content this kernel cannot see.
func mapStopReason(status, incomplete string) (wire.StopReason, string) {
	switch status {
	case "", "completed":
		return wire.StopStop, ""
	case "incomplete":
		switch incomplete {
		case "max_output_tokens":
			return wire.StopLength, ""
		case "":
			return wire.StopError, "Response incomplete without a provider reason"
		default:
			return wire.StopError, "Response incomplete: " + incomplete
		}
	case "failed", "cancelled":
		return wire.StopError, "Response failed"
	case "in_progress", "queued":
		// Neither is a real ending, but the API can report them as the last
		// status; treating them as a stop keeps a completed turn from failing.
		return wire.StopStop, ""
	default:
		return wire.StopError, "Unhandled stop reason: " + status
	}
}

func incompleteReason(response *streamResponse) string {
	if response == nil || response.IncompleteDetails == nil {
		return ""
	}
	return response.IncompleteDetails.Reason
}

func decodeItem(raw json.RawMessage) (item, error) {
	var parsed item
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return item{}, fmt.Errorf("responses: decode item: %w", err)
	}
	return parsed, nil
}

// missingTail reports the part of final the reader has not seen. A string that is
// not an extension of what arrived is reported as absent: a delta cannot take
// text back, so the caller keeps what was already delivered.
func missingTail(delivered, final string) (string, bool) {
	if final == delivered || !strings.HasPrefix(final, delivered) {
		return "", false
	}
	return final[len(delivered):], true
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
			events, done, feedErr := s.dec.feed(event.Data)
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

// complete ends a stream that stopped without a terminal response event. Half a
// message must not look whole, so this fails instead of closing the blocks.
func (s *stream) complete() {
	if s.finalized {
		s.terminal = true
		return
	}
	s.fail(errors.New("responses: stream ended before a terminal response event"))
}

func (s *stream) fail(err error) {
	reason := wire.StopError
	message := err.Error()
	if s.ctx.Err() != nil {
		reason = wire.StopAborted
		message = "responses: request aborted"
	}
	s.queue = append(s.queue, s.dec.frame.Error(reason, message))
	s.finalized = true
	s.terminal = true
}

// Close releases the response body. Recv is not safe for concurrent use, which
// matches provider.Stream.
func (s *stream) Close() error { return s.body.Close() }

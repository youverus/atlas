package google

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

// chunk is one `data:` payload of a streaming generateContent response. The
// candidates array is indexed by choice; atlas reads the first one, because it
// never asks for several.
type chunk struct {
	Candidates    []candidate  `json:"candidates"`
	UsageMetadata *usageMetric `json:"usageMetadata"`
	ModelVersion  string       `json:"modelVersion"`
	ResponseID    string       `json:"responseId"`
	Error         *chunkError  `json:"error"`
}

type candidate struct {
	Content      *candidateContent `json:"content"`
	FinishReason string            `json:"finishReason"`
	Index        int               `json:"index"`
}

type candidateContent struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

type chunkError struct {
	Message string `json:"message"`
	Status  string `json:"status"`
	Code    int    `json:"code"`
}

func (e *chunkError) Error() string {
	if e.Status == "" {
		return "google: stream error: " + e.Message
	}
	return fmt.Sprintf("google: stream error (%s): %s", e.Status, e.Message)
}

// usageMetric is this API's token accounting. The prompt count includes the
// cached part, and the answer count includes the thought summary, so both are
// split out here to match the shape the rest of the tree expects.
type usageMetric struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

func (u usageMetric) toWire() wire.Usage {
	input := u.PromptTokenCount - u.CachedContentTokenCount
	if input < 0 {
		input = 0
	}
	out := wire.Usage{
		Input:       input,
		Output:      u.CandidatesTokenCount + u.ThoughtsTokenCount,
		CacheRead:   u.CachedContentTokenCount,
		TotalTokens: u.TotalTokenCount,
	}
	if u.ThoughtsTokenCount > 0 {
		reasoning := u.ThoughtsTokenCount
		out.Reasoning = &reasoning
	}
	if out.TotalTokens == 0 {
		// A gateway may omit the total; every other counter is present, so the
		// sum is the answer.
		out.TotalTokens = input + out.Output + out.CacheRead
	}
	return out
}

// mapStopReason mirrors pi: STOP is a clean finish, MAX_TOKENS a length stop,
// and every filtered or malformed reason a failure rather than a silent end.
func mapStopReason(reason string) (wire.StopReason, string) {
	switch reason {
	case "STOP", "":
		return wire.StopStop, ""
	case "MAX_TOKENS":
		return wire.StopLength, ""
	default:
		return wire.StopError, "Provider finish reason: " + reason
	}
}

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockToolCall
)

// decoder turns chunks into frame calls. This API streams a part's text in
// pieces but a function call whole, so a call arrives as start, one delta and
// end in the same chunk.
type decoder struct {
	frame *provider.Frame
	kinds []blockKind

	chunks int

	finishSeen  bool
	stopReason  wire.StopReason
	stopMessage string

	// calls numbers the tool calls of this response, which is how a synthetic id
	// stays unique inside the message.
	calls int
}

func newDecoder(model wire.Model) *decoder {
	return &decoder{frame: provider.NewFrame(model)}
}

// feed decodes one data payload. This API has no [DONE] sentinel: the stream
// ends when the connection closes.
func (d *decoder) feed(data string) ([]wire.AssistantEvent, error) {
	if strings.TrimSpace(data) == "" {
		return nil, nil
	}
	var chunk chunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, fmt.Errorf("google: decode chunk: %w", err)
	}
	d.chunks++
	if chunk.Error != nil {
		return nil, chunk.Error
	}
	if chunk.UsageMetadata != nil {
		d.frame.SetUsage(chunk.UsageMetadata.toWire())
	}
	if len(chunk.Candidates) == 0 {
		// A usage-only trailer carries no content.
		return nil, nil
	}

	first := chunk.Candidates[0]
	var events []wire.AssistantEvent
	if first.Content != nil {
		for _, piece := range first.Content.Parts {
			events = append(events, d.part(piece)...)
		}
	}
	if first.FinishReason != "" {
		d.finishSeen = true
		d.stopReason, d.stopMessage = mapStopReason(first.FinishReason)
	}
	return events, nil
}

// part dispatches one piece of a turn. A thought summary, an answer and a
// function call are three different blocks, and consecutive pieces of the same
// kind extend the block they are already in.
func (d *decoder) part(piece part) []wire.AssistantEvent {
	switch {
	case piece.FunctionCall != nil:
		return d.toolCall(*piece.FunctionCall, piece.ThoughtSignature)
	case piece.Thought:
		return d.thinkingDelta(piece.Text, piece.ThoughtSignature)
	case piece.Text != "":
		return d.textDelta(piece.Text, piece.ThoughtSignature)
	default:
		return nil
	}
}

// textDelta extends the trailing text block or opens a new one, which is how a
// text run interrupted by a tool call becomes two blocks.
func (d *decoder) textDelta(delta, signature string) []wire.AssistantEvent {
	index, kind, ok := d.last()
	if !ok || kind != blockText {
		index = d.alloc(blockText)
		start := d.frame.TextStart(index)
		// The signature only sticks once the block carries its type, so it is
		// recorded between opening the block and its first delta.
		d.frame.TextSignature(index, signature)
		return []wire.AssistantEvent{start, d.frame.TextDelta(index, delta)}
	}
	// Some deployments only send the signature with the first piece of a block.
	d.frame.TextSignature(index, signature)
	return []wire.AssistantEvent{d.frame.TextDelta(index, delta)}
}

func (d *decoder) thinkingDelta(delta, signature string) []wire.AssistantEvent {
	index, kind, ok := d.last()
	if !ok || kind != blockThinking {
		index = d.alloc(blockThinking)
		start := d.frame.ThinkingStart(index)
		d.frame.ThinkingSignature(index, signature)
		return []wire.AssistantEvent{start, d.frame.ThinkingDelta(index, delta)}
	}
	d.frame.ThinkingSignature(index, signature)
	return []wire.AssistantEvent{d.frame.ThinkingDelta(index, delta)}
}

// toolCall emits a whole call at once. The arguments arrive as a finished
// object here, so the delta carries the entire JSON document.
func (d *decoder) toolCall(call functionCall, signature string) []wire.AssistantEvent {
	d.calls++
	id := call.ID
	if id == "" {
		// This API only started requiring an id with Gemini 3, so an older model
		// leaves it out. A synthesized one keeps the call and its result paired.
		id = callID(fmt.Sprintf("%s_%d", call.Name, d.calls))
	} else {
		id = callID(id)
	}

	index := d.alloc(blockToolCall)
	arguments := call.Args
	if len(arguments) == 0 {
		arguments = json.RawMessage("{}")
	}
	start := d.frame.ToolCallStart(index, id, call.Name)
	d.frame.ThoughtSignature(index, signature)
	return []wire.AssistantEvent{start, d.frame.ToolCallDelta(index, string(arguments))}
}

func (d *decoder) alloc(kind blockKind) int {
	d.kinds = append(d.kinds, kind)
	return len(d.kinds) - 1
}

func (d *decoder) last() (int, blockKind, bool) {
	if len(d.kinds) == 0 {
		return 0, 0, false
	}
	return len(d.kinds) - 1, d.kinds[len(d.kinds)-1], true
}

// finalize closes every open block and emits the terminal event.
func (d *decoder) finalize() ([]wire.AssistantEvent, error) {
	events := make([]wire.AssistantEvent, 0, len(d.kinds)+1)
	for index, kind := range d.kinds {
		switch kind {
		case blockText:
			events = append(events, d.frame.TextEnd(index))
		case blockThinking:
			events = append(events, d.frame.ThinkingEnd(index))
		case blockToolCall:
			event, err := d.frame.ToolCallEnd(index)
			if err != nil {
				return nil, fmt.Errorf("google: %w", err)
			}
			events = append(events, event)
		}
	}

	if d.finishSeen {
		if d.stopReason == wire.StopError {
			return append(events, d.frame.Error(wire.StopError, d.stopMessage)), nil
		}
		reason := d.stopReason
		// A turn that produced a call stopped to use it, whatever the reason
		// said: the frame reports the call, not the transport's opinion.
		if reason == wire.StopStop && d.hasToolCall() {
			reason = wire.StopToolUse
		}
		return append(events, d.frame.Done(reason)), nil
	}

	if d.chunks == 0 {
		return nil, errors.New("google: response contained no stream chunks")
	}
	return nil, errors.New("google: stream ended without a finish reason")
}

func (d *decoder) hasToolCall() bool {
	for _, kind := range d.kinds {
		if kind == blockToolCall {
			return true
		}
	}
	return false
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
		case event.Name != "" && event.Name != "message":
			// This API names every event "message" or leaves the name out.
			// Skipping anything else keeps a gateway keep-alive from
			// desynchronising the frame.
		default:
			events, feedErr := s.dec.feed(event.Data)
			if feedErr != nil {
				s.pending = feedErr
				continue
			}
			s.queue = append(s.queue, events...)
		}
	}
}

func (s *stream) complete() {
	if s.finalized {
		s.terminal = true
		return
	}
	s.finalized = true
	events, err := s.dec.finalize()
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
		message = "google: request aborted"
	}
	s.queue = append(s.queue, s.dec.frame.Error(reason, message))
	s.finalized = true
	s.terminal = true
}

// Close releases the response body. Recv is not safe for concurrent use, which
// matches provider.Stream.
func (s *stream) Close() error { return s.body.Close() }

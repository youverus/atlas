package openai

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

// chunk is one `data:` payload of a completions stream.
type chunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *chunkUsage   `json:"usage"`
	Error   *chunkError   `json:"error"`
}

type chunkChoice struct {
	Delta chunkDelta `json:"delta"`
	// FinishReason is a pointer because null means "not finished yet".
	FinishReason *string `json:"finish_reason"`
	// Usage is a fallback for gateways that nest it inside the choice.
	Usage *chunkUsage `json:"usage"`
}

type chunkDelta struct {
	Content          *string         `json:"content"`
	ToolCalls        []chunkToolCall `json:"tool_calls"`
	ReasoningContent *string         `json:"reasoning_content"`
	Reasoning        *string         `json:"reasoning"`
	ReasoningText    *string         `json:"reasoning_text"`
}

type chunkToolCall struct {
	// Index is the position within this message, not a content index.
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chunkError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (e *chunkError) Error() string {
	if e.Type == "" {
		return "openai: stream error: " + e.Message
	}
	return fmt.Sprintf("openai: stream error (%s): %s", e.Type, e.Message)
}

type chunkUsage struct {
	PromptTokens            int                `json:"prompt_tokens"`
	CompletionTokens        int                `json:"completion_tokens"`
	CachedTokens            *int               `json:"cached_tokens"`
	PromptCacheHitTokens    *int               `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails     *promptDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails *completionDetails `json:"completion_tokens_details"`
}

type promptDetails struct {
	CachedTokens     *int `json:"cached_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens"`
}

type completionDetails struct {
	ReasoningTokens *int `json:"reasoning_tokens"`
}

// toWire follows pi's accounting: cached and written tokens are reported
// separately and subtracted from the prompt total, because compatible vendors
// put the cache counters in three different places.
func (u chunkUsage) toWire() wire.Usage {
	cacheRead := 0
	switch {
	case u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil:
		cacheRead = *u.PromptTokensDetails.CachedTokens
	case u.PromptCacheHitTokens != nil:
		cacheRead = *u.PromptCacheHitTokens
	case u.CachedTokens != nil:
		cacheRead = *u.CachedTokens
	}
	cacheWrite := 0
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CacheWriteTokens != nil {
		cacheWrite = *u.PromptTokensDetails.CacheWriteTokens
	}
	input := u.PromptTokens - cacheRead - cacheWrite
	if input < 0 {
		input = 0
	}
	out := wire.Usage{
		Input:       input,
		Output:      u.CompletionTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: input + u.CompletionTokens + cacheRead + cacheWrite,
	}
	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != nil {
		if reasoning := *u.CompletionTokensDetails.ReasoningTokens; reasoning > 0 {
			out.Reasoning = &reasoning
		}
	}
	return out
}

// mapStopReason mirrors pi: an unknown or filtered finish reason is a failure,
// not a silent stop.
func mapStopReason(reason string) (wire.StopReason, string) {
	switch reason {
	case "stop", "end":
		return wire.StopStop, ""
	case "length":
		return wire.StopLength, ""
	case "function_call", "tool_calls":
		return wire.StopToolUse, ""
	default:
		return wire.StopError, "Provider finish_reason: " + reason
	}
}

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockToolCall
)

// decoder turns chunks into frame calls. It owns the mapping from provider
// indices to content indices, which the frame deliberately does not guess.
type decoder struct {
	frame  *provider.Frame
	kinds  []blockKind
	toolAt map[int]int

	chunks   int
	tolerate bool

	finishSeen  bool
	stopReason  wire.StopReason
	stopMessage string
}

func newDecoder(model wire.Model, tolerateMissingFinish bool) *decoder {
	return &decoder{
		frame:    provider.NewFrame(model),
		toolAt:   map[int]int{},
		tolerate: tolerateMissingFinish,
	}
}

// feed decodes one data payload. The bool reports the `[DONE]` sentinel.
func (d *decoder) feed(data string) ([]wire.AssistantEvent, bool, error) {
	if strings.TrimSpace(data) == "[DONE]" {
		return nil, true, nil
	}

	var chunk chunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, false, fmt.Errorf("openai: decode chunk: %w", err)
	}
	d.chunks++
	if chunk.Error != nil {
		return nil, false, chunk.Error
	}
	if chunk.Usage != nil {
		d.setUsage(*chunk.Usage)
	}
	if len(chunk.Choices) == 0 {
		// A usage-only trailer carries no delta.
		return nil, false, nil
	}

	choice := chunk.Choices[0]
	if chunk.Usage == nil && choice.Usage != nil {
		d.setUsage(*choice.Usage)
	}
	var events []wire.AssistantEvent
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		d.finishSeen = true
		d.stopReason, d.stopMessage = mapStopReason(*choice.FinishReason)
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		events = append(events, d.textDelta(*choice.Delta.Content)...)
	}
	if field, value := choice.Delta.reasoning(); field != "" {
		events = append(events, d.thinkingDelta(field, value)...)
	}
	for _, call := range choice.Delta.ToolCalls {
		events = append(events, d.toolCallDelta(call)...)
	}
	return events, false, nil
}

// reasoning returns the first non-empty reasoning field. Some gateways emit two
// copies of the same text under different names; taking the first avoids
// duplicating the block.
func (d chunkDelta) reasoning() (string, string) {
	for _, candidate := range []struct {
		field string
		value *string
	}{
		{"reasoning_content", d.ReasoningContent},
		{"reasoning", d.Reasoning},
		{"reasoning_text", d.ReasoningText},
	} {
		if candidate.value != nil && *candidate.value != "" {
			return candidate.field, *candidate.value
		}
	}
	return "", ""
}

func (d *decoder) setUsage(raw chunkUsage) { d.frame.SetUsage(raw.toWire()) }

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

// textDelta reuses the trailing text block, matching pi: a text run interrupted
// by a tool call becomes two blocks instead of one.
func (d *decoder) textDelta(delta string) []wire.AssistantEvent {
	index, kind, ok := d.last()
	if !ok || kind != blockText {
		index = d.alloc(blockText)
		return []wire.AssistantEvent{
			d.frame.TextStart(index),
			d.frame.TextDelta(index, delta),
		}
	}
	return []wire.AssistantEvent{d.frame.TextDelta(index, delta)}
}

func (d *decoder) thinkingDelta(field, delta string) []wire.AssistantEvent {
	index, kind, ok := d.last()
	if !ok || kind != blockThinking {
		index = d.alloc(blockThinking)
		start := d.frame.ThinkingStart(index)
		d.frame.ThinkingSignature(index, field)
		return []wire.AssistantEvent{start, d.frame.ThinkingDelta(index, delta)}
	}
	// A single block keeps one signature; the first field wins.
	d.frame.ThinkingSignature(index, field)
	return []wire.AssistantEvent{d.frame.ThinkingDelta(index, delta)}
}

func (d *decoder) toolCallDelta(call chunkToolCall) []wire.AssistantEvent {
	if index, ok := d.toolAt[call.Index]; ok {
		var events []wire.AssistantEvent
		if call.ID != "" || call.Function.Name != "" {
			// The first fragment only carries the index for some providers.
			d.frame.ToolCallIdentify(index, call.ID, call.Function.Name)
		}
		if call.Function.Arguments != "" {
			events = append(events, d.frame.ToolCallDelta(index, call.Function.Arguments))
		}
		return events
	}
	index := d.alloc(blockToolCall)
	d.toolAt[call.Index] = index
	events := []wire.AssistantEvent{d.frame.ToolCallStart(index, call.ID, call.Function.Name)}
	if call.Function.Arguments != "" {
		events = append(events, d.frame.ToolCallDelta(index, call.Function.Arguments))
	}
	return events
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
			message := d.frame.Message()
			block := message.Content.At(index)
			if block == nil || block.ID == "" {
				return nil, fmt.Errorf("openai: tool call %d arrived without an id", index)
			}
			event, err := d.frame.ToolCallEnd(index)
			if err != nil {
				return nil, fmt.Errorf("openai: %w", err)
			}
			events = append(events, event)
		}
	}

	if d.finishSeen {
		if d.stopReason == wire.StopError {
			return append(events, d.frame.Error(wire.StopError, d.stopMessage)), nil
		}
		return append(events, d.frame.Done(d.stopReason)), nil
	}

	// No finish_reason: infer only when the vendor is known to omit it.
	if d.tolerate {
		reason := wire.StopStop
		for _, kind := range d.kinds {
			if kind == blockToolCall {
				reason = wire.StopToolUse
				break
			}
		}
		return append(events, d.frame.Done(reason)), nil
	}
	if d.chunks == 0 {
		return nil, errors.New("openai: response contained no stream chunks")
	}
	return nil, errors.New("openai: stream ended without finish_reason")
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

func newStream(ctx context.Context, model wire.Model, body io.ReadCloser, tolerateMissingFinish bool) *stream {
	s := &stream{
		ctx:    ctx,
		dec:    newDecoder(model, tolerateMissingFinish),
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
		case event.Name != "":
			// Completions streams carry no event names. Skipping unknown named
			// events keeps a gateway keep-alive from desynchronising the frame.
		default:
			events, done, feedErr := s.dec.feed(event.Data)
			if feedErr != nil {
				s.pending = feedErr
				continue
			}
			s.queue = append(s.queue, events...)
			if done {
				s.complete()
			}
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
		message = "openai: request aborted"
	}
	s.queue = append(s.queue, s.dec.frame.Error(reason, message))
	s.finalized = true
	s.terminal = true
}

// Close releases the response body. Recv is not safe for concurrent use, which
// matches provider.Stream.
func (s *stream) Close() error { return s.body.Close() }

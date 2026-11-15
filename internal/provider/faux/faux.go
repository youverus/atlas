// Package faux is a deterministic provider for tests and demos. It replays
// scripted responses and never touches the network.
package faux

import (
	"context"
	"io"
	"sync"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// Response is one scripted model turn.
type Response struct {
	Thinking   string
	Text       string
	ToolCalls  []wire.Content
	StopReason wire.StopReason
	Usage      wire.Usage
	Err        error
	// ChunkSize is the number of runes per delta. Defaults to 8.
	ChunkSize int
}

// Provider replays responses in order and records every request it saw.
type Provider struct {
	mu        sync.Mutex
	responses []Response
	calls     []provider.Request
}

// New creates a provider from a response script.
func New(responses ...Response) *Provider {
	return &Provider{responses: append([]Response(nil), responses...)}
}

// API implements provider.Provider.
func (p *Provider) API() string { return "faux" }

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.calls = append(p.calls, req)
	var response Response
	if len(p.responses) > 0 {
		response = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	return &stream{ctx: ctx, events: build(req.Model, response)}, nil
}

// Calls returns the requests received so far.
func (p *Provider) Calls() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.calls...)
}

// Pending reports how many scripted responses are left.
func (p *Provider) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.responses)
}

type stream struct {
	ctx    context.Context
	events []wire.AssistantEvent
	next   int
	closed bool
}

func (s *stream) Recv() (wire.AssistantEvent, error) {
	if err := s.ctx.Err(); err != nil {
		return wire.AssistantEvent{}, err
	}
	if s.closed || s.next >= len(s.events) {
		return wire.AssistantEvent{}, io.EOF
	}
	event := s.events[s.next]
	s.next++
	return event, nil
}

func (s *stream) Close() error {
	s.closed = true
	return nil
}

func build(model wire.Model, response Response) []wire.AssistantEvent {
	frame := provider.NewFrame(model)
	if response.Usage != (wire.Usage{}) {
		frame.SetUsage(response.Usage)
	}
	events := []wire.AssistantEvent{frame.Start()}

	index := 0
	if response.Thinking != "" {
		events = append(events, frame.ThinkingStart(index))
		for _, chunk := range chunks(response.Thinking, response.ChunkSize) {
			events = append(events, frame.ThinkingDelta(index, chunk))
		}
		events = append(events, frame.ThinkingEnd(index))
		index++
	}
	if response.Text != "" {
		events = append(events, frame.TextStart(index))
		for _, chunk := range chunks(response.Text, response.ChunkSize) {
			events = append(events, frame.TextDelta(index, chunk))
		}
		events = append(events, frame.TextEnd(index))
		index++
	}
	for _, call := range response.ToolCalls {
		events = append(events, frame.ToolCallStart(index, call.ID, call.Name))
		arguments := string(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		events = append(events, frame.ToolCallDelta(index, arguments))
		end, err := frame.ToolCallEnd(index)
		if err != nil {
			events = append(events, frame.Error(wire.StopError, err.Error()))
			return events
		}
		events = append(events, end)
		index++
	}

	if response.Err != nil {
		events = append(events, frame.Error(wire.StopError, response.Err.Error()))
		return events
	}
	reason := response.StopReason
	if reason == "" {
		if len(response.ToolCalls) > 0 {
			reason = wire.StopToolUse
		} else {
			reason = wire.StopStop
		}
	}
	events = append(events, frame.Done(reason))
	return events
}

func chunks(text string, size int) []string {
	if size <= 0 {
		size = 8
	}
	runes := []rune(text)
	var out []string
	for len(runes) > 0 {
		n := size
		if n > len(runes) {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

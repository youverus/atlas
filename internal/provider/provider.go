// Package provider defines the model-facing boundary: a request, a stream of
// assistant events, and the interface every wire adapter implements.
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"pi-go/internal/wire"
)

// Request is one model call. Messages are already normalized: the leading
// system message carries the prompt and tool declarations.
type Request struct {
	Model    wire.Model
	Messages []wire.Message
	Options  Options
}

// Options carries per-call knobs. Adapters ignore fields they do not support.
type Options struct {
	Temperature   *float64
	MaxTokens     *int
	ThinkingLevel string
	APIKey        string
	SessionID     string
	Headers       map[string]string
}

// Stream yields assistant events. Recv returns io.EOF after the terminal event
// (done or error). Callers must Close the stream when they stop reading.
type Stream interface {
	Recv() (wire.AssistantEvent, error)
	Close() error
}

// Provider is one wire API family, such as openai-completions.
//
// Stream must be safe to call concurrently: a side conversation runs next to the
// main one and shares the same adapter instance.
type Provider interface {
	API() string
	Stream(ctx context.Context, req Request) (Stream, error)
}

var errStreamIdleTimeout = errors.New("model response stream idle timeout")

type idleTimeoutProvider struct {
	Provider
	timeout time.Duration
}

// WithStreamIdleTimeout bounds the wait for each decoded event from a provider
// whose stream observes cancellation of its request context.
func WithStreamIdleTimeout(p Provider, timeout time.Duration) Provider {
	return idleTimeoutProvider{Provider: p, timeout: timeout}
}

func (p idleTimeoutProvider) Stream(ctx context.Context, req Request) (Stream, error) {
	streamCtx, cancel := context.WithCancelCause(ctx)
	stream, err := p.Provider.Stream(streamCtx, req)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	return &idleTimeoutStream{Stream: stream, ctx: streamCtx, cancel: cancel, timeout: p.timeout}, nil
}

type idleTimeoutStream struct {
	Stream
	ctx      context.Context
	cancel   context.CancelCauseFunc
	timeout  time.Duration
	timedOut bool
}

func (s *idleTimeoutStream) Recv() (wire.AssistantEvent, error) {
	if s.timedOut {
		return wire.AssistantEvent{}, io.EOF
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(s.timeout, func() {
		s.cancel(errStreamIdleTimeout)
		close(expired)
	})
	event, err := s.Stream.Recv()
	if timer.Stop() {
		return event, err
	}
	<-expired
	if context.Cause(s.ctx) != errStreamIdleTimeout {
		return event, err
	}
	s.timedOut = true
	timeoutErr := fmt.Errorf("%w after %s", errStreamIdleTimeout, s.timeout)
	if event.Type != wire.EventError || event.Error == nil {
		return wire.AssistantEvent{}, timeoutErr
	}
	// Adapters see the request context close and return an aborted message.
	// Keep their partial output and usage while reporting the timeout.
	message := event.Error.Clone()
	message.StopReason = wire.StopError
	message.ErrorCode = wire.ErrorCodeStreamIdleTimeout
	message.ErrorMessage = timeoutErr.Error()
	event.Reason = string(wire.StopError)
	event.Error = &message
	return event, nil
}

func (s *idleTimeoutStream) Close() error {
	s.cancel(nil)
	return s.Stream.Close()
}

// Collect drains a stream and returns the terminal assistant message plus every
// event it saw. It is the simple path for tests and non-streaming callers.
func Collect(stream Stream) (wire.Message, []wire.AssistantEvent, error) {
	defer stream.Close()
	var events []wire.AssistantEvent
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return wire.Message{}, events, err
		}
		events = append(events, event)
		switch event.Type {
		case wire.EventDone:
			if event.Message != nil {
				return *event.Message, events, nil
			}
		case wire.EventError:
			if event.Error != nil {
				return *event.Error, events, nil
			}
		}
	}
	return wire.Message{}, events, errors.New("provider: stream ended without a terminal event")
}

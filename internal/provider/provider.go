// Package provider defines the model-facing boundary: a request, a stream of
// assistant events, and the interface every wire adapter implements.
package provider

import (
	"context"
	"errors"
	"io"

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

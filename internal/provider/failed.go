package provider

import (
	"io"

	"pi-go/internal/wire"
)

// FailedStream returns the stream an adapter uses when it has no response body
// to read: the opening event followed by one terminal error. Adapters must
// still return a stream instead of a Go error for transport and HTTP failures,
// so callers keep a single read path.
func FailedStream(model wire.Model, reason wire.StopReason, message string) Stream {
	frame := NewFrame(model)
	return &failedStream{events: []wire.AssistantEvent{frame.Start(), frame.Error(reason, message)}}
}

type failedStream struct {
	events []wire.AssistantEvent
	next   int
}

func (s *failedStream) Recv() (wire.AssistantEvent, error) {
	if s.next >= len(s.events) {
		return wire.AssistantEvent{}, io.EOF
	}
	event := s.events[s.next]
	s.next++
	return event, nil
}

func (s *failedStream) Close() error { return nil }

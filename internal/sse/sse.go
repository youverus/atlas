// Package sse parses Server-Sent Events, the framing used by the OpenAI chat
// completions and Anthropic messages streaming APIs, and by the MCP streamable
// HTTP transport.
//
// It sits at internal/ root rather than under provider/ because it has two
// unrelated consumers and belongs to neither: the model adapters frame model
// streams with it, and internal/mcp frames tool responses with it. Nothing in
// here knows what a provider is.
//
// The parser follows the WHATWG event-stream rules with three deliberate
// choices:
//
//   - A trailing event that has no terminating blank line is dispatched at end
//     of input instead of being discarded. Gateways sometimes omit the final
//     blank line, and that event is often the terminator (`data: [DONE]`), so
//     dropping it would hang the caller.
//   - Event names are reported verbatim. The spec's default of "message" is not
//     substituted, so a caller can tell "no event field" from an explicit one.
//   - A bare carriage return is not treated as a line break. Real providers use
//     LF or CRLF.
//
// The reader is not context-aware. Cancellation works by closing the underlying
// response body, which makes the pending Read return.
package sse

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DefaultMaxLineSize bounds a single line so a malformed stream cannot consume
// unbounded memory. Provider chunks are normally well under 64 KiB.
const DefaultMaxLineSize = 4 << 20

// ErrLineTooLong reports a line larger than the configured limit.
var ErrLineTooLong = errors.New("sse: line exceeds the maximum size")

// Event is one dispatched server-sent event.
type Event struct {
	// ID is the id field of this event, empty when the stream did not set one.
	// See Reader.LastEventID for the value that persists across events.
	ID string
	// Name is the event field, empty when the stream did not set one.
	Name string
	// Data joins every data field of the event with a newline.
	Data string
	// Retry is the reconnection delay in milliseconds, zero when absent.
	Retry int
}

// Reader turns an event stream into Events. It is not safe for concurrent use.
type Reader struct {
	scanner   *bufio.Scanner
	maxLine   int
	firstLine bool
	lastID    string
	err       error
}

// NewReader reads from r with DefaultMaxLineSize.
func NewReader(r io.Reader) *Reader {
	return NewReaderSize(r, DefaultMaxLineSize)
}

// NewReaderSize reads from r with an explicit line limit. A limit of zero or
// less selects DefaultMaxLineSize.
func NewReaderSize(r io.Reader, maxLine int) *Reader {
	if maxLine <= 0 {
		maxLine = DefaultMaxLineSize
	}
	// Provider chunks are a few KiB, so start where bufio starts and let the
	// scanner grow instead of paying the limit up front on every stream.
	initial := 4 << 10
	if initial > maxLine {
		initial = maxLine
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initial), maxLine)
	return &Reader{scanner: scanner, maxLine: maxLine, firstLine: true}
}

// Next returns the next event. It returns io.EOF once the stream is exhausted.
// A read error is sticky: every later call reports it again rather than
// degrading to io.EOF, so a truncated stream can never look complete.
func (r *Reader) Next() (Event, error) {
	if r.err != nil {
		return Event{}, r.err
	}

	var event Event
	var data []string
	hasData := false

	for {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				r.err = r.lineError(err)
				return Event{}, r.err
			}
			r.err = io.EOF
			if hasData {
				return r.dispatch(event, data), nil
			}
			return Event{}, io.EOF
		}

		line := r.scanner.Text()
		if r.firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			r.firstLine = false
		}

		if line == "" {
			// A blank line dispatches the event, unless no data field was seen:
			// then it only resets the partially built event.
			if !hasData {
				event = Event{}
				continue
			}
			return r.dispatch(event, data), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, hasColon := strings.Cut(line, ":")
		if !hasColon {
			field, value = line, ""
		} else {
			// Exactly one leading space is part of the framing, not the value.
			value = strings.TrimPrefix(value, " ")
		}

		switch field {
		case "event":
			event.Name = value
		case "data":
			data = append(data, value)
			hasData = true
		case "id":
			if !strings.ContainsRune(value, 0) {
				event.ID = value
			}
		case "retry":
			if millis, err := strconv.Atoi(value); err == nil && millis >= 0 {
				event.Retry = millis
			}
		}
	}
}

// LastEventID returns the most recent non-empty id field seen by the reader.
func (r *Reader) LastEventID() string { return r.lastID }

func (r *Reader) dispatch(event Event, data []string) Event {
	event.Data = strings.Join(data, "\n")
	if event.ID != "" {
		r.lastID = event.ID
	}
	return event
}

func (r *Reader) lineError(err error) error {
	if errors.Is(err, bufio.ErrTooLong) {
		return fmt.Errorf("%w (limit %d bytes)", ErrLineTooLong, r.maxLine)
	}
	return err
}

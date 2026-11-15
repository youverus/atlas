package rpc

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLineReaderSplitsOnNewlines(t *testing.T) {
	reader := newLineReader(strings.NewReader("one\ntwo\r\nthree"))

	for _, want := range []string{"one", "two", "three"} {
		line, err := reader.next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if string(line) != want {
			t.Fatalf("line = %q, want %q", line, want)
		}
	}
	if _, err := reader.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

// A JSON string may contain U+2028 and U+2029, which readline-style splitters
// treat as line breaks and would cut the record in half.
func TestLineReaderKeepsUnicodeSeparators(t *testing.T) {
	record := "{\"message\":\"a\u2028b\u2029c\"}"
	reader := newLineReader(strings.NewReader(record + "\n"))

	line, err := reader.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if string(line) != record {
		t.Fatalf("line = %q, want the whole record %q", line, record)
	}
}

// An empty line is a record: it fails to parse and the protocol answers with a
// parse error, which is how pi reports a blank command.
func TestLineReaderReportsEmptyLines(t *testing.T) {
	reader := newLineReader(strings.NewReader("\n\n"))
	for i := 0; i < 2; i++ {
		line, err := reader.next()
		if err != nil {
			t.Fatalf("next %d: %v", i, err)
		}
		if len(line) != 0 {
			t.Fatalf("line = %q, want empty", line)
		}
	}
}

func TestWriterEmitsOneRecordPerLine(t *testing.T) {
	var out bytes.Buffer
	writer := newWriter(&out)

	if err := writer.write(map[string]any{"type": "response", "text": "a<b&c"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.write("second"); err != nil {
		t.Fatalf("write: %v", err)
	}

	want := "{\"text\":\"a<b&c\",\"type\":\"response\"}\n\"second\"\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// failingWriter rejects every write, standing in for a client that closed the
// pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWriterRemembersAFailure(t *testing.T) {
	writer := newWriter(failingWriter{})
	if err := writer.write(1); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("err = %v, want the pipe error", err)
	}
	if err := writer.write(2); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("second err = %v, want the same sticky error", err)
	}
	if err := writer.failed(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failed = %v, want the pipe error", err)
	}
}

// brokenReader fails after one record, standing in for a stream that dies
// mid-conversation.
type brokenReader struct {
	line string
	done bool
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.done {
		return 0, io.ErrUnexpectedEOF
	}
	b.done = true
	n := copy(p, b.line+"\n")
	return n, nil
}

func TestLineReaderReportsStreamErrors(t *testing.T) {
	reader := newLineReader(&brokenReader{line: "one"})

	line, err := reader.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if string(line) != "one" {
		t.Fatalf("line = %q, want one", line)
	}
	if _, err := reader.next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want the stream error", err)
	}
}

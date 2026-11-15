package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// lineReader splits an input stream into JSONL records.
//
// Framing is LF-only and matches pi's attachJsonlLineReader: a readline-style
// splitter also breaks on U+2028 and U+2029, which are legal inside JSON
// strings, so a record would be cut in half. A trailing CR is dropped because a
// client that writes CRLF still means one record, and a final record without a
// newline is delivered when the stream ends instead of being dropped.
type lineReader struct {
	reader *bufio.Reader
}

func newLineReader(in io.Reader) *lineReader {
	return &lineReader{reader: bufio.NewReader(in)}
}

// next returns the next record. An empty line is a record too: it fails to
// parse and the protocol answers with a parse error, which is how pi reports
// it. io.EOF means the stream is finished.
func (r *lineReader) next() ([]byte, error) {
	line, err := r.reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return nil, io.EOF
	}
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}

// writer serializes JSONL output.
//
// Two writers exist at any time: the command loop answering a request and the
// turn goroutine streaming events. One mutex keeps a record whole, and one
// encoder keeps the bytes close to what JSON.stringify produces: HTML escaping
// is off because pi does not escape <, >, or &, and a client that echoes text
// back should see the same bytes it sent.
type writer struct {
	mu      sync.Mutex
	encoder *json.Encoder
	err     error
}

func newWriter(out io.Writer) *writer {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return &writer{encoder: encoder}
}

// write emits one record. Once a write fails the error is sticky: a broken
// stream cannot be repaired, and every later record would fail the same way.
func (w *writer) write(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if err := w.encoder.Encode(value); err != nil {
		w.err = err
		return err
	}
	return nil
}

// failed reports a sticky write error, so the read loop can stop as soon as the
// client is gone.
func (w *writer) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

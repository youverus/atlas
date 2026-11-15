package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// readBufferSize matches pi's 1 MiB read buffer; a session line can hold a
	// large tool output, so the reader must not have a token limit.
	readBufferSize = 1 << 20
	// headerScanLimit bounds how much is read when only the header is needed.
	headerScanLimit = 1 << 20
)

// ErrNotSession reports a non-empty file that does not start with a session
// header. Callers must not rewrite such a file.
var ErrNotSession = errors.New("session: file is not a pi session")

// ErrHeaderTooLarge reports a header that exceeds headerScanLimit. Discovery
// treats it as unreadable; Open falls back to a full load.
var ErrHeaderTooLarge = errors.New("session: header exceeds scan limit")

// File is a parsed session file. An empty file yields a zero Header.
type File struct {
	Header  Header
	Entries []Entry // excludes the header

	// Migrated reports that entries were upgraded on load, so the caller should
	// rewrite the file to persist the new version.
	Migrated bool

	sawHeader bool
}

// Empty reports whether the file had no session header.
func (f *File) Empty() bool { return !f.sawHeader }

// LoadFile parses a session file. Blank and malformed lines are skipped, as pi
// does: a truncated write at the tail must not hide the history before it.
func LoadFile(path string) (*File, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	file := &File{}
	reader := bufio.NewReaderSize(handle, readBufferSize)
	needNewline := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			needNewline = !bytes.HasSuffix(line, []byte("\n"))
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if err := file.addLine(trimmed); err != nil {
					return nil, err
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("session: read %s: %w", path, readErr)
		}
	}

	// pi appends the missing terminator so the next append starts a new line
	// instead of gluing itself to the last entry.
	if needNewline {
		if err := appendBytes(path, []byte("\n")); err != nil {
			return nil, err
		}
	}
	if file.sawHeader && migrate(&file.Header, file.Entries) {
		file.Migrated = true
	}
	return file, nil
}

func (f *File) addLine(line []byte) error {
	if !f.sawHeader {
		var probe struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			// Malformed lines before the header are skipped, matching pi's loader.
			return nil
		}
		if probe.Type != "session" || probe.ID == "" {
			return ErrNotSession
		}
		header, err := decodeHeader(line)
		if err != nil {
			return ErrNotSession
		}
		f.sawHeader = true
		f.Header = header
		return nil
	}
	entry, err := decodeEntry(line)
	if err != nil {
		// Malformed entries are skipped rather than failing the whole session.
		return nil
	}
	f.Entries = append(f.Entries, entry)
	return nil
}

// encode renders the whole file as JSONL.
func (f *File) encode() ([]byte, error) {
	var buffer bytes.Buffer
	header, err := json.Marshal(f.Header)
	if err != nil {
		return nil, fmt.Errorf("session: encode header: %w", err)
	}
	buffer.Write(header)
	buffer.WriteByte('\n')
	for _, entry := range f.Entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("session: encode entry %s: %w", entry.ID, err)
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

// EncodeFile renders a session file as JSONL. It is exported for the callers
// that write those bytes somewhere else — an export that hands the current
// branch to someone — so that "what a session file looks like" keeps one answer.
func EncodeFile(file *File) ([]byte, error) { return file.encode() }

// SaveFile writes the header and every entry, replacing the file. Entries that
// were not modified keep their original bytes.
func SaveFile(path string, file *File) error {
	data, err := file.encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("session: create directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("session: write %s: %w", path, err)
	}
	return nil
}

// CreateFile writes a new session file, refusing to overwrite an existing one.
// pi uses the exclusive flag so two runs racing for the same timestamped name
// cannot clobber history.
func CreateFile(path string, file *File) error {
	data, err := file.encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("session: create directory: %w", err)
	}
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("session: create %s: %w", path, err)
	}
	if _, err := handle.Write(data); err != nil {
		handle.Close()
		return fmt.Errorf("session: write %s: %w", path, err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("session: close %s: %w", path, err)
	}
	return nil
}

// AppendEntry writes one entry as a new line.
func AppendEntry(path string, entry Entry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("session: encode entry %s: %w", entry.ID, err)
	}
	return appendBytes(path, append(encoded, '\n'))
}

func appendBytes(path string, data []byte) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("session: open %s: %w", path, err)
	}
	if _, err := handle.Write(data); err != nil {
		handle.Close()
		return fmt.Errorf("session: append %s: %w", path, err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("session: close %s: %w", path, err)
	}
	return nil
}

// ScanHeader reads only as far as the first parsable entry. It returns nil when
// that entry is not a session header, and ErrHeaderTooLarge when the header does
// not fit in headerScanLimit bytes.
func ScanHeader(path string) (*Header, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	reader := bufio.NewReaderSize(handle, 4096)
	scanned := 0
	for scanned < headerScanLimit {
		line, readErr := reader.ReadBytes('\n')
		scanned += len(line)
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var probe struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			}
			if err := json.Unmarshal(trimmed, &probe); err == nil {
				if probe.Type != "session" || probe.ID == "" {
					return nil, nil
				}
				header, err := decodeHeader(trimmed)
				if err != nil {
					return nil, nil
				}
				return &header, nil
			}
		}
		if readErr == io.EOF {
			return nil, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	// A header ending exactly at the limit is still readable: probe for EOF
	// before declaring the file too large.
	if _, err := reader.Peek(1); err == nil {
		return nil, ErrHeaderTooLarge
	}
	return nil, nil
}

package shell

import (
	"fmt"
	"os"
	"strings"

	"pi-go/internal/tool"
)

// accumulator tracks streaming output with bounded memory. Only a tail is kept
// for the display snapshot; everything else spills into a temp file the moment
// the output would exceed a limit, so a command that prints a gigabyte costs a
// gigabyte of disk rather than of RAM.
//
// The zero value is not usable; call newAccumulator.
type accumulator struct {
	maxLines        int
	maxBytes        int
	maxRollingBytes int
	tempFilePrefix  string
	// spill decides where the file holding the untruncated output goes. A nil
	// value is fine: it means the OS temp directory.
	spill *tool.Spill

	rawChunks [][]byte
	tail      []byte
	tailBytes int
	// tailStartsAtLineBoundary records whether the byte before the retained tail
	// was a newline. When the trim landed mid-line, the first partial line is
	// dropped from the snapshot so the output does not start with a fragment.
	tailStartsAtLineBoundary bool

	totalRawBytes     int
	totalDecodedBytes int
	completedLines    int
	totalLines        int
	currentLineBytes  int
	hasOpenLine       bool
	finished          bool

	tempFilePath string
	tempFile     *os.File
}

type accumulatorOptions struct {
	maxLines       int
	maxBytes       int
	tempFilePrefix string
	spill          *tool.Spill
}

type outputSnapshot struct {
	content        string
	truncation     tool.Truncation
	fullOutputPath string
}

type fullOutput struct {
	content   string
	truncated bool
}

func newAccumulator(options accumulatorOptions) *accumulator {
	maxLines, maxBytes := options.maxLines, options.maxBytes
	if maxLines <= 0 {
		maxLines = tool.DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = tool.DefaultMaxBytes
	}
	prefix := options.tempFilePrefix
	if prefix == "" {
		prefix = "pi-bash"
	}
	return &accumulator{
		maxLines:                 maxLines,
		maxBytes:                 maxBytes,
		maxRollingBytes:          max(maxBytes*2, 1),
		tempFilePrefix:           prefix,
		spill:                    options.spill,
		tailStartsAtLineBoundary: true,
	}
}

func (a *accumulator) append(data []byte) {
	if a.finished {
		// Output arriving after the command ended is dropped rather than
		// corrupting the snapshot.
		return
	}
	a.totalRawBytes += len(data)
	a.appendText(data)

	if a.tempFile != nil || a.shouldUseTempFile() {
		a.ensureTempFile()
		if a.tempFile != nil {
			_, _ = a.tempFile.Write(data)
		}
		return
	}
	if len(data) > 0 {
		chunk := make([]byte, len(data))
		copy(chunk, data)
		a.rawChunks = append(a.rawChunks, chunk)
	}
}

func (a *accumulator) finish() {
	if a.finished {
		return
	}
	a.finished = true
	if a.shouldUseTempFile() {
		a.ensureTempFile()
	}
}

// snapshot renders what the model sees. persistIfTruncated forces the temp file
// into existence so the notice can name a path that actually exists.
func (a *accumulator) snapshot(persistIfTruncated bool) outputSnapshot {
	truncation := tool.TruncateTail(a.snapshotText(), tool.Limits{MaxLines: a.maxLines, MaxBytes: a.maxBytes})
	truncated := a.totalLines > a.maxLines || a.totalDecodedBytes > a.maxBytes
	truncatedBy := truncation.TruncatedBy
	if truncatedBy == "" {
		// TruncateTail reports nothing when it kept everything, but the counters
		// can still show that earlier output was dropped by the rolling tail.
		if a.totalDecodedBytes > a.maxBytes {
			truncatedBy = "bytes"
		} else {
			truncatedBy = "lines"
		}
	}
	truncation.Truncated = truncated
	truncation.TruncatedBy = truncatedBy
	truncation.TotalLines = a.totalLines
	truncation.TotalBytes = a.totalDecodedBytes
	truncation.MaxLines = a.maxLines
	truncation.MaxBytes = a.maxBytes

	if persistIfTruncated && truncated {
		a.ensureTempFile()
	}
	return outputSnapshot{
		content:        truncation.Content,
		truncation:     truncation,
		fullOutputPath: a.tempFilePath,
	}
}

func (a *accumulator) closeTempFile() error {
	if a.tempFile == nil {
		return nil
	}
	file := a.tempFile
	a.tempFile = nil
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// readFullOutput returns the complete output for a programmatic caller, bounded
// by maxBytes. Output past that budget keeps its head and tail around an
// omission marker, and the marker counts the omitted bytes so a caller can tell
// how much it did not see.
func (a *accumulator) readFullOutput(maxBytes int) (fullOutput, error) {
	if a.tempFilePath == "" {
		var builder strings.Builder
		for _, chunk := range a.rawChunks {
			builder.Write(chunk)
		}
		return fullOutput{content: builder.String()}, nil
	}

	file, err := os.Open(a.tempFilePath)
	if err != nil {
		return fullOutput{}, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fullOutput{}, err
	}
	size := info.Size()
	if size <= int64(maxBytes) {
		data, err := os.ReadFile(a.tempFilePath)
		if err != nil {
			return fullOutput{}, err
		}
		return fullOutput{content: string(data)}, nil
	}

	headBytes := maxBytes / 2
	tailBytes := maxBytes - headBytes
	head := make([]byte, headBytes)
	tail := make([]byte, tailBytes)
	if _, err := file.ReadAt(head, 0); err != nil {
		return fullOutput{}, err
	}
	if _, err := file.ReadAt(tail, size-int64(tailBytes)); err != nil {
		return fullOutput{}, err
	}

	// Cut at rune boundaries: the head drops an incomplete trailing sequence and
	// the tail skips leading continuation bytes.
	headText := trimIncompleteRune(string(head))
	tailStart := 0
	for tailStart < len(tail) && tail[tailStart]&0xc0 == 0x80 {
		tailStart++
	}
	omitted := size - int64(headBytes) - int64(tailBytes)
	return fullOutput{
		content:   fmt.Sprintf("%s\n\n[... %d bytes omitted ...]\n\n%s", headText, omitted, string(tail[tailStart:])),
		truncated: true,
	}, nil
}

// getLastLineBytes is the size of the line currently being written, which a
// partial-line notice reports so the model knows how wide that line really is.
func (a *accumulator) getLastLineBytes() int { return a.currentLineBytes }

func (a *accumulator) appendText(text []byte) {
	if len(text) == 0 {
		return
	}
	a.totalDecodedBytes += len(text)
	a.tail = append(a.tail, text...)
	a.tailBytes += len(text)
	if a.tailBytes > a.maxRollingBytes*2 {
		a.trimTail()
	}

	newlines := 0
	lastNewline := -1
	for i, b := range text {
		if b == '\n' {
			newlines++
			lastNewline = i
		}
	}
	if newlines == 0 {
		a.currentLineBytes += len(text)
		a.hasOpenLine = true
	} else {
		a.completedLines += newlines
		remainder := text[lastNewline+1:]
		a.currentLineBytes = len(remainder)
		a.hasOpenLine = len(remainder) > 0
	}
	a.totalLines = a.completedLines
	if a.hasOpenLine {
		a.totalLines++
	}
}

func (a *accumulator) trimTail() {
	if len(a.tail) <= a.maxRollingBytes {
		a.tailBytes = len(a.tail)
		return
	}
	start := len(a.tail) - a.maxRollingBytes
	for start < len(a.tail) && a.tail[start]&0xc0 == 0x80 {
		start++
	}
	if start > 0 {
		a.tailStartsAtLineBoundary = a.tail[start-1] == '\n'
	}
	trimmed := make([]byte, len(a.tail)-start)
	copy(trimmed, a.tail[start:])
	a.tail = trimmed
	a.tailBytes = len(trimmed)
}

func (a *accumulator) snapshotText() string {
	if a.tailStartsAtLineBoundary {
		return string(a.tail)
	}
	if index := indexByte(a.tail, '\n'); index >= 0 {
		return string(a.tail[index+1:])
	}
	return string(a.tail)
}

func (a *accumulator) shouldUseTempFile() bool {
	return a.totalRawBytes > a.maxBytes || a.totalDecodedBytes > a.maxBytes || a.totalLines > a.maxLines
}

func (a *accumulator) ensureTempFile() {
	if a.tempFilePath != "" {
		return
	}
	file, err := a.spill.Create(a.tempFilePrefix, ".log")
	if err != nil {
		// Without a temp file the model still gets the truncated tail, so a
		// failure here must not abort the command's result.
		return
	}
	a.tempFilePath = file.Name()
	a.tempFile = file
	for _, chunk := range a.rawChunks {
		if _, err := file.Write(chunk); err != nil {
			break
		}
	}
	a.rawChunks = nil
}

func indexByte(data []byte, b byte) int {
	for i, value := range data {
		if value == b {
			return i
		}
	}
	return -1
}

// trimIncompleteRune drops a trailing partial UTF-8 sequence.
func trimIncompleteRune(text string) string {
	for length := 0; length < 4 && length < len(text); length++ {
		last := text[len(text)-1-length]
		if last&0xc0 == 0x80 {
			continue
		}
		size := 1
		switch {
		case last&0x80 == 0:
			size = 1
		case last&0xe0 == 0xc0:
			size = 2
		case last&0xf0 == 0xe0:
			size = 3
		case last&0xf8 == 0xf0:
			size = 4
		}
		if size == length+1 {
			return text
		}
		return text[:len(text)-length-1]
	}
	return text
}

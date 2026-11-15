package fs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadText(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "line one\nline two\nline three"})
	result := run(t, pick(t, cwd, "read"), `{"path":"a.txt"}`)

	if got := text(t, result); got != "line one\nline two\nline three" {
		t.Fatalf("read returned %q", got)
	}
	if result.Details != nil {
		t.Fatalf("an untruncated read should not set details: %s", result.Details)
	}
	// structuredContent is the plain text for a text file.
	var structured string
	if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
		t.Fatalf("structuredContent is not a JSON string: %v", err)
	}
	if structured != "line one\nline two\nline three" {
		t.Fatalf("structuredContent = %q", structured)
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "l1\nl2\nl3\nl4\nl5"})

	result := run(t, pick(t, cwd, "read"), `{"path":"a.txt","offset":2,"limit":2}`)
	got := text(t, result)
	// A user limit that stops early advertises the next offset.
	if !strings.HasPrefix(got, "l2\nl3") {
		t.Fatalf("read with offset/limit returned %q", got)
	}
	if !strings.Contains(got, "[2 more lines in file. Use offset=4 to continue.]") {
		t.Fatalf("missing continuation notice: %q", got)
	}

	result = run(t, pick(t, cwd, "read"), `{"path":"a.txt","offset":5}`)
	if got := text(t, result); got != "l5" {
		t.Fatalf("read at the last line returned %q", got)
	}
}

func TestReadOffsetBeyondEnd(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "l1\nl2"})
	err := runError(t, pick(t, cwd, "read"), `{"path":"a.txt","offset":9}`)
	want := "Offset 9 is beyond end of file (2 lines total)"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestReadTruncatesLongFile(t *testing.T) {
	var lines []string
	for i := 1; i <= 2500; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	cwd := newCwd(t, map[string]string{"big.txt": strings.Join(lines, "\n")})

	result := run(t, pick(t, cwd, "read"), `{"path":"big.txt"}`)
	got := text(t, result)

	if !strings.HasSuffix(got, "[Showing lines 1-2000 of 2500. Use offset=2001 to continue.]") {
		t.Fatalf("missing line truncation notice, tail is %q", got[max(0, len(got)-120):])
	}
	if result.Details == nil {
		t.Fatal("a truncated read must set details")
	}
	var details readDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.Truncation == nil || details.Truncation.OutputLines != 2000 {
		t.Fatalf("details truncation = %+v", details.Truncation)
	}
	if details.Truncation.TruncatedBy != "lines" {
		t.Fatalf("truncatedBy = %q", details.Truncation.TruncatedBy)
	}
}

// A first line larger than the byte budget cannot be shown at all, so read
// points at a command that can cut it.
func TestReadFirstLineExceedsByteLimit(t *testing.T) {
	cwd := newCwd(t, map[string]string{"wide.txt": strings.Repeat("x", 60*1024) + "\nsecond"})

	result := run(t, pick(t, cwd, "read"), `{"path":"wide.txt"}`)
	got := text(t, result)
	if !strings.HasPrefix(got, "[Line 1 is 60.0KB, exceeds 50.0KB limit. Use bash: sed -n '1p' wide.txt | head -c 51200]") {
		t.Fatalf("unexpected first-line notice: %q", got)
	}
}

func TestReadImage(t *testing.T) {
	png := append(append([]byte{}, pngSignature...),
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R', 0x00, 0x00, 0x01, 0x00)
	cwd := newCwd(t, map[string]string{})
	if err := os.WriteFile(filepath.Join(cwd, "pic.png"), png, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}

	result := run(t, pick(t, cwd, "read"), `{"path":"pic.png"}`)
	if len(result.Content) != 2 {
		t.Fatalf("an image read must return a note and an image block, got %d blocks", len(result.Content))
	}
	if result.Content[0].Text != "Read image file [image/png]" {
		t.Fatalf("note = %q", result.Content[0].Text)
	}
	if result.Content[1].Type != "image" || result.Content[1].MIMEType != "image/png" {
		t.Fatalf("image block = %+v", result.Content[1])
	}
	if result.Content[1].Data == "" {
		t.Fatal("image block carries no data")
	}

	var structured readStructuredImage
	if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
		t.Fatalf("structuredContent: %v", err)
	}
	if structured.Type != "image" || structured.MIMEType != "image/png" || structured.Note == "" {
		t.Fatalf("structuredContent = %+v", structured)
	}
}

func TestReadMissingFile(t *testing.T) {
	cwd := newCwd(t, map[string]string{})
	err := runError(t, pick(t, cwd, "read"), `{"path":"nope.txt"}`)
	if !strings.Contains(err.Error(), "nope.txt") {
		t.Fatalf("error should name the path: %v", err)
	}
}

func TestReadRequiresPath(t *testing.T) {
	cwd := newCwd(t, map[string]string{"a.txt": "x"})
	runError(t, pick(t, cwd, "read"), `{}`)
}

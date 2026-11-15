package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveToCwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	cwd := "/work/project"

	tests := []struct {
		name string
		path string
		want string
	}{
		{"relative", "src/main.go", "/work/project/src/main.go"},
		{"dot", ".", "/work/project"},
		{"parent", "../other/file.go", "/work/other/file.go"},
		{"absolute", "/etc/hosts", "/etc/hosts"},
		{"tilde", "~/notes.txt", filepath.Join(home, "notes.txt")},
		{"tilde alone", "~", home},
		{"unicode space", "a\u00a0b.txt", "/work/project/a b.txt"},
		{"narrow space", "a\u202fb.txt", "/work/project/a b.txt"},
		{"clean", "a/./b/../c.txt", "/work/project/a/c.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveToCwd(test.path, cwd); got != test.want {
				t.Fatalf("resolveToCwd(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

// The read path repairs the spellings macOS uses for screenshots. The variants
// are only tried when the literal path is missing, so a real file always wins.
func TestResolveReadPathVariants(t *testing.T) {
	root := newCwd(t, map[string]string{
		"Capture d\u2019\u00e9cran.txt": "curly and nfd",
		"Screen Shot 2024-01-02 PM.txt": "plain",
	})

	if got := resolveReadPath("Capture d'écran.txt", root); got != filepath.Join(root, "Capture d\u2019\u00e9cran.txt") {
		t.Fatalf("curly/NFD variant not resolved, got %q", got)
	}

	// A literal match is returned untouched even when a variant also exists.
	if got := resolveReadPath("Screen Shot 2024-01-02 PM.txt", root); got != filepath.Join(root, "Screen Shot 2024-01-02 PM.txt") {
		t.Fatalf("literal path should not be rewritten, got %q", got)
	}
}

func TestLineEndings(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"lf", "a\nb\n", "\n"},
		{"crlf", "a\r\nb\r\n", "\r\n"},
		{"mixed starts crlf", "a\r\nb\n", "\r\n"},
		{"mixed starts lf", "a\nb\r\n", "\n"},
		{"no newline", "abc", "\n"},
		{"empty", "", "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := detectLineEnding(test.content); got != test.want {
				t.Fatalf("detectLineEnding(%q) = %q, want %q", test.content, got, test.want)
			}
		})
	}

	if got := normalizeToLF("a\r\nb\rc\n"); got != "a\nb\nc\n" {
		t.Fatalf("normalizeToLF = %q", got)
	}
	if got := restoreLineEndings("a\nb\n", "\r\n"); got != "a\r\nb\r\n" {
		t.Fatalf("restoreLineEndings = %q", got)
	}
	if got := restoreLineEndings("a\nb\n", "\n"); got != "a\nb\n" {
		t.Fatalf("restoreLineEndings with LF changed content: %q", got)
	}
}

func TestSplitBOM(t *testing.T) {
	bom, text := splitBOM("\ufeffhello")
	if bom != "\ufeff" || text != "hello" {
		t.Fatalf("splitBOM = (%q, %q)", bom, text)
	}
	bom, text = splitBOM("hello")
	if bom != "" || text != "hello" {
		t.Fatalf("splitBOM without a BOM = (%q, %q)", bom, text)
	}
}

func TestNormalizeForFuzzyMatch(t *testing.T) {
	input := "  it\u2019s \u201cquoted\u201d \u2014 ok   \r\nsecond\u00a0line\t\n"
	got := normalizeForFuzzyMatch(input)

	// Trailing whitespace is stripped per line.
	if strings.Contains(got, "ok   ") {
		t.Fatalf("trailing whitespace survived: %q", got)
	}
	// Smart quotes and dashes become ASCII.
	if !strings.Contains(got, "it's \"quoted\" - ok") {
		t.Fatalf("quotes or dashes not normalized: %q", got)
	}
	// A non-breaking space becomes a plain space.
	if !strings.Contains(got, "second line") {
		t.Fatalf("non-breaking space not normalized: %q", got)
	}
	// Line count is preserved so offsets stay usable.
	if strings.Count(got, "\n") != strings.Count(input, "\n") {
		t.Fatalf("line count changed: %q", got)
	}
}

func TestSplitLinesWithEndings(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"no newline", "abc", []string{"abc"}},
		{"trailing newline", "a\nb\n", []string{"a\n", "b\n"}},
		{"no trailing newline", "a\nb", []string{"a\n", "b"}},
		{"blank line", "a\n\nb", []string{"a\n", "\n", "b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := splitLinesWithEndings(test.content)
			if len(got) != len(test.want) {
				t.Fatalf("splitLinesWithEndings(%q) = %q, want %q", test.content, got, test.want)
			}
			if joined := strings.Join(got, ""); joined != test.content {
				t.Fatalf("rejoining changed the content: %q", joined)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("line %d = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestDetectImageMimeType(t *testing.T) {
	png := append(append([]byte{}, pngSignature...),
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R', 0x00, 0x00, 0x01, 0x00)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	jpegLossless := []byte{0xff, 0xd8, 0xff, 0xf7, 0x00}
	gif := []byte("GIF89a\x01\x00\x01\x00")
	webp := append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WEBP")...)...)
	bmpHeader := make([]byte, 30)
	bmpHeader[0], bmpHeader[1] = 'B', 'M'
	bmpHeader[2] = 100
	bmpHeader[10] = 54
	bmpHeader[14] = 40
	bmpHeader[26] = 1
	bmpHeader[28] = 24

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", png, "image/png"},
		{"jpeg", jpeg, "image/jpeg"},
		{"jpeg lossless rejected", jpegLossless, ""},
		{"gif", gif, "image/gif"},
		{"webp", webp, "image/webp"},
		{"bmp", bmpHeader, "image/bmp"},
		{"text", []byte("hello world"), ""},
		{"empty", nil, ""},
		{"truncated png", pngSignature, ""},
		{"bm prefix only", []byte("BM not really a bitmap"), ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := detectImageMimeType(test.data); got != test.want {
				t.Fatalf("detectImageMimeType = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDetectImageMimeTypeRejectsAnimatedPNG(t *testing.T) {
	build := func(chunks ...string) []byte {
		data := append([]byte{}, pngSignature...)
		data = append(data, 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R')
		data = append(data, make([]byte, 13)...)
		data = append(data, 0, 0, 0, 0)
		for _, chunk := range chunks {
			data = append(data, 0x00, 0x00, 0x00, 0x01)
			data = append(data, []byte(chunk)...)
			data = append(data, 0, 0, 0, 0)
		}
		return data
	}

	if got := detectImageMimeType(build("IDAT")); got != "image/png" {
		t.Fatalf("a plain PNG should be accepted, got %q", got)
	}
	if got := detectImageMimeType(build("acTL", "IDAT")); got != "" {
		t.Fatalf("an animated PNG should be rejected, got %q", got)
	}
}

func TestToSlash(t *testing.T) {
	if got := toSlash("a/b"); got != "a/b" {
		t.Fatalf("toSlash = %q", got)
	}
	if filepath.Separator != '/' {
		if got := toSlash("a" + string(filepath.Separator) + "b"); got != "a/b" {
			t.Fatalf("toSlash = %q", got)
		}
	}
}

package assembly

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
	"pi-go/internal/wire"
)

const (
	// AttachmentDirName is the per-session directory storing uploaded GUI attachments.
	AttachmentDirName = "attachments"

	maxAttachmentImageBytes = 5 << 20  // 5 MiB
	maxAttachmentPDFBytes   = 15 << 20 // 15 MiB
	maxAttachmentTextBytes  = 512 << 10
	maxInlineTextBytes      = 64 << 10
)

// AttachmentInput describes one file uploaded by the GUI composer.
type AttachmentInput struct {
	Kind     string
	Name     string
	MIMEType string
	Data     string
}

// BuildUserMessageWithAttachments converts a typed prompt and optional GUI
// attachments (images, PDFs, text files) into a multimodal wire.Message.
func BuildUserMessageWithAttachments(ctx context.Context, prompt string, attachments []AttachmentInput, attachDir string) (wire.Message, error) {
	trimmed := strings.TrimSpace(prompt)
	if len(attachments) == 0 {
		return wire.UserMessage(prompt), nil
	}

	var blocks []wire.Content
	labels := make([]string, 0, len(attachments))
	for _, item := range attachments {
		name := sanitizeAttachmentName(item.Name)
		labels = append(labels, name)
		made, err := buildAttachmentBlocks(ctx, item, name, attachDir)
		if err != nil {
			return wire.Message{}, err
		}
		blocks = append(blocks, made...)
	}

	lead := prompt
	if trimmed == "" {
		lead = "[附件: " + strings.Join(labels, ", ") + "]"
	}
	return wire.UserMessageBlocks(append([]wire.Content{wire.TextContent(lead)}, blocks...)...), nil
}

func buildAttachmentBlocks(ctx context.Context, item AttachmentInput, safeName, attachDir string) ([]wire.Content, error) {
	switch strings.ToLower(strings.TrimSpace(item.Kind)) {
	case "image":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.Data))
		if err != nil {
			return nil, fmt.Errorf("image %s: invalid base64 payload: %w", safeName, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("image %s is empty", safeName)
		}
		if len(raw) > maxAttachmentImageBytes {
			return nil, fmt.Errorf("image %s is %s; max allowed is %s", safeName, tool.FormatSize(len(raw)), tool.FormatSize(maxAttachmentImageBytes))
		}
		mimeType := fs.ImageMimeType(raw)
		if mimeType == "" {
			return nil, fmt.Errorf("image %s has an unsupported image format (only PNG, JPEG, GIF, WebP are supported)", safeName)
		}
		labelName := saveAttachmentFile(attachDir, safeName, raw)
		return []wire.Content{
			wire.TextContent(fmt.Sprintf(`<file name="%s"></file>`, labelName)),
			wire.ImageContent(base64.StdEncoding.EncodeToString(raw), mimeType),
		}, nil

	case "pdf":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.Data))
		if err != nil {
			return nil, fmt.Errorf("pdf %s: invalid base64 payload: %w", safeName, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("pdf %s is empty", safeName)
		}
		if len(raw) > maxAttachmentPDFBytes {
			return nil, fmt.Errorf("pdf %s is %s; max allowed is %s", safeName, tool.FormatSize(len(raw)), tool.FormatSize(maxAttachmentPDFBytes))
		}
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("%PDF-")) {
			return nil, fmt.Errorf("%s is not a valid PDF file", safeName)
		}
		savedPath := saveAttachmentFile(attachDir, safeName, raw)
		extracted := extractPDFContent(ctx, savedPath, raw)
		if strings.TrimSpace(extracted) == "" {
			note := fmt.Sprintf("[PDF attached (%s); no extractable plain text found — it may be a scanned/image PDF. Saved at %s]", tool.FormatSize(len(raw)), savedPath)
			return []wire.Content{
				wire.TextContent(fmt.Sprintf("<file name=\"%s\" type=\"pdf\">\n%s\n</file>", savedPath, note)),
			}, nil
		}
		body, truncated := truncateUTF8(extracted, maxInlineTextBytes)
		if truncated {
			body += fmt.Sprintf("\n\n[truncated at %s; full PDF saved at %s]", tool.FormatSize(maxInlineTextBytes), savedPath)
		}
		return []wire.Content{
			wire.TextContent(fmt.Sprintf("<file name=\"%s\" type=\"pdf\">\n%s\n</file>", savedPath, body)),
		}, nil

	case "text":
		raw := []byte(item.Data)
		if len(raw) > maxAttachmentTextBytes {
			return nil, fmt.Errorf("text file %s is %s; max allowed is %s", safeName, tool.FormatSize(len(raw)), tool.FormatSize(maxAttachmentTextBytes))
		}
		if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
			return nil, fmt.Errorf("file %s is not valid UTF-8 text", safeName)
		}
		labelName := saveAttachmentFile(attachDir, safeName, raw)
		body, truncated := truncateUTF8(item.Data, maxInlineTextBytes)
		if truncated {
			body += fmt.Sprintf("\n\n[truncated at %s; full file saved at %s]", tool.FormatSize(maxInlineTextBytes), labelName)
		}
		return []wire.Content{
			wire.TextContent(fmt.Sprintf("<file name=\"%s\">\n%s\n</file>", labelName, body)),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported attachment kind %q (only image, pdf, text are supported)", item.Kind)
	}
}

func sanitizeAttachmentName(raw string) string {
	base := filepath.Base(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")))
	if base == "" || base == "." || base == "/" || base == ".." {
		return "attachment"
	}
	var b strings.Builder
	for _, r := range base {
		if r < 0x20 || r == '"' || r == '<' || r == '>' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func saveAttachmentFile(attachDir, name string, data []byte) string {
	if strings.TrimSpace(attachDir) == "" {
		return name
	}
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		return name
	}
	target := filepath.Join(attachDir, name)
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		target = filepath.Join(attachDir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext))
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return name
	}
	return target
}

func truncateUTF8(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

func extractPDFContent(ctx context.Context, savedPath string, raw []byte) string {
	if pdftotext, err := exec.LookPath("pdftotext"); err == nil && savedPath != "" && filepath.IsAbs(savedPath) {
		runCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(runCtx, pdftotext, "-layout", "-enc", "UTF-8", savedPath, "-")
		if out, err := cmd.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 && utf8.Valid(out) {
			return string(out)
		}
	}
	return extractPDFTextFallback(raw)
}

var pdfStreamRe = regexp.MustCompile(`(?s)<<(.*?)>>\s*stream\r?\n(.*?)\r?\n?endstream`)

// extractPDFTextFallback performs best-effort pure-stdlib text extraction from
// uncompressed and FlateDecode PDF content streams when pdftotext is absent.
func extractPDFTextFallback(raw []byte) string {
	var out strings.Builder
	matches := pdfStreamRe.FindAllSubmatch(raw, -1)
	for _, m := range matches {
		dict := m[1]
		streamData := m[2]
		if bytes.Contains(dict, []byte("/FlateDecode")) {
			zr, err := zlib.NewReader(bytes.NewReader(streamData))
			if err != nil {
				continue
			}
			decompressed, err := io.ReadAll(io.LimitReader(zr, 4<<20))
			_ = zr.Close()
			if err != nil && len(decompressed) == 0 {
				continue
			}
			streamData = decompressed
		}
		if text := parsePDFTextOperators(streamData); text != "" {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(text)
			if out.Len() >= maxInlineTextBytes*2 {
				break
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func parsePDFTextOperators(stream []byte) string {
	s := string(stream)
	var b strings.Builder
	inText := false
	for i := 0; i < len(s); i++ {
		if !inText {
			if s[i] == 'B' && i+1 < len(s) && s[i+1] == 'T' && isPDFDelim(s, i-1) && isPDFDelim(s, i+2) {
				inText = true
				i++
			}
			continue
		}
		if s[i] == 'E' && i+1 < len(s) && s[i+1] == 'T' && isPDFDelim(s, i-1) && isPDFDelim(s, i+2) {
			inText = false
			b.WriteByte('\n')
			i++
			continue
		}
		if s[i] == '(' {
			lit, next := readPDFLiteralString(s, i)
			i = next
			if lit != "" {
				b.WriteString(lit)
			}
			continue
		}
		if s[i] == 'T' && i+1 < len(s) && (s[i+1] == 'd' || s[i+1] == 'D' || s[i+1] == '*') && isPDFDelim(s, i-1) && isPDFDelim(s, i+2) {
			b.WriteByte('\n')
			i++
		}
	}
	return strings.TrimSpace(b.String())
}

func isPDFDelim(s string, idx int) bool {
	if idx < 0 || idx >= len(s) {
		return true
	}
	c := s[idx]
	return c <= ' ' || c == '(' || c == ')' || c == '<' || c == '>' || c == '[' || c == ']' || c == '/'
}

func readPDFLiteralString(s string, start int) (string, int) {
	var b strings.Builder
	depth := 1
	i := start + 1
	for i < len(s) && depth > 0 {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '(', ')', '\\':
				b.WriteByte(s[i])
			default:
				b.WriteByte(s[i])
			}
			i++
			continue
		}
		if ch == '(' {
			depth++
			b.WriteByte(ch)
		} else if ch == ')' {
			depth--
			if depth == 0 {
				return b.String(), i
			}
			b.WriteByte(ch)
		} else {
			b.WriteByte(ch)
		}
		i++
	}
	return b.String(), i
}

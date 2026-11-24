package assembly

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/wire"
)

func TestBuildUserMessageWithAttachmentsImageTextAndPDF(t *testing.T) {
	dir := t.TempDir()

	// 1x1 valid PNG bytes (IHDR + IDAT + IEND)
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
		0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54,
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0d, 0x0a, 0x2d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
		0xae, 0x42, 0x60, 0x82,
	}

	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write([]byte("BT /F1 12 Tf 72 720 Td (Hello PDF Atlas) Tj ET"))
	_ = zw.Close()
	pdfBytes := append([]byte("%PDF-1.4\n1 0 obj\n<< /Filter /FlateDecode /Length "), []byte(fmt.Sprintf("%d >>\nstream\n", compressed.Len()))...)
	pdfBytes = append(pdfBytes, compressed.Bytes()...)
	pdfBytes = append(pdfBytes, []byte("\nendstream\nendobj\n%%EOF\n")...)

	msg, err := BuildUserMessageWithAttachments(context.Background(), "Summarize these", []AttachmentInput{
		{Kind: "image", Name: "shot.png", Data: base64.StdEncoding.EncodeToString(pngBytes)},
		{Kind: "text", Name: "notes.md", Data: "# Notes\nLine two"},
		{Kind: "pdf", Name: "doc.pdf", Data: base64.StdEncoding.EncodeToString(pdfBytes)},
	}, dir)
	if err != nil {
		t.Fatalf("BuildUserMessageWithAttachments: %v", err)
	}

	items := msg.Content.Items()
	if len(items) != 5 {
		t.Fatalf("expected 5 content blocks, got %d: %+v", len(items), items)
	}
	if items[0].Type != wire.ContentText || items[0].Text != "Summarize these" {
		t.Fatalf("unexpected lead block: %+v", items[0])
	}
	if items[2].Type != wire.ContentImage || items[2].MIMEType != "image/png" {
		t.Fatalf("expected image/png block, got %+v", items[2])
	}
	if !strings.Contains(items[3].Text, "# Notes\nLine two") {
		t.Fatalf("expected inline text attachment, got %q", items[3].Text)
	}
	if !strings.Contains(items[4].Text, "Hello PDF Atlas") {
		t.Fatalf("expected extracted PDF text, got %q", items[4].Text)
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.pdf")); err != nil {
		t.Fatalf("expected saved PDF file: %v", err)
	}
}

func TestBuildUserMessageWithAttachmentsRejectsUnsupportedKind(t *testing.T) {
	_, err := BuildUserMessageWithAttachments(context.Background(), "", []AttachmentInput{
		{Kind: "zip", Name: "bad.zip", Data: "AAAA"},
	}, "")
	if err == nil {
		t.Fatal("expected unsupported kind error")
	}
}

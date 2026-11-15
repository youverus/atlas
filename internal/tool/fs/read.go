package fs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const readDescription = "Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). " +
	"Images are sent as attachments. For text files, output is truncated to " +
	"2000 lines or 50KB (whichever is hit first). Use offset/limit for large files. " +
	"When you need the full file, continue with offset until complete."

const readParameters = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path to the file to read (relative or absolute)"},
    "offset": {"type": "number", "description": "Line number to start reading from (1-indexed)"},
    "limit": {"type": "number", "description": "Maximum number of lines to read"}
  },
  "required": ["path"]
}`

type readTool struct{ set *shared }

func (t readTool) Name() string                { return "read" }
func (t readTool) Label() string               { return "read" }
func (t readTool) Description() string         { return readDescription }
func (t readTool) Parameters() json.RawMessage { return json.RawMessage(readParameters) }

type readRequest struct {
	Path   string       `json:"path"`
	Offset tool.Number  `json:"offset"`
	Limit  *tool.Number `json:"limit"`
}

// readDetails is the details payload. It is omitted entirely when nothing was
// truncated, matching pi.
type readDetails struct {
	Truncation *tool.Truncation `json:"truncation,omitempty"`
}

// readStructuredImage is the structuredContent shape for an image read.
type readStructuredImage struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MIMEType string `json:"mimeType"`
	Note     string `json:"note"`
}

func (t readTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request readRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(request.Path) == "" {
		return tool.Result{}, fmt.Errorf("read: path is required")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}

	absolute := resolveReadPath(request.Path, t.set.cwd)
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return tool.Result{}, err
	}

	// The file is read before its type is known, exactly as pi does: one read
	// serves both the image and the text path, so a file cannot change between
	// the sniff and the payload.
	if mimeType := detectImageMimeType(headOf(raw)); mimeType != "" {
		return t.imageResult(mimeType, base64.StdEncoding.EncodeToString(raw)), nil
	}

	return t.textResult(string(raw), request)
}

func (t readTool) imageResult(mimeType, data string) tool.Result {
	note := fmt.Sprintf("Read image file [%s]", mimeType)
	structured, err := json.Marshal(readStructuredImage{
		Type:     "image",
		Data:     data,
		MIMEType: mimeType,
		Note:     note,
	})
	if err != nil {
		// Marshaling a struct of strings cannot fail; keep the shape total.
		structured = json.RawMessage(`{}`)
	}
	return tool.Result{
		Content: []wire.Content{
			wire.TextContent(note),
			wire.ImageContent(data, mimeType),
		},
		StructuredContent: structured,
	}
}

// textResult applies offset and limit, then the shared head truncation.
func (t readTool) textResult(text string, request readRequest) (tool.Result, error) {
	lines := strings.Split(text, "\n")
	totalFileLines := len(lines)

	startLine := 0
	if request.Offset.Int() > 0 {
		startLine = max(0, request.Offset.Int()-1)
	}
	startLineDisplay := startLine + 1
	if startLine >= len(lines) {
		return tool.Result{}, fmt.Errorf(
			"Offset %d is beyond end of file (%d lines total)", request.Offset.Int(), len(lines))
	}

	var selected string
	var userLimitedLines int
	userLimited := request.Limit != nil
	if userLimited {
		endLine := min(startLine+request.Limit.Int(), len(lines))
		selected = strings.Join(lines[startLine:endLine], "\n")
		userLimitedLines = endLine - startLine
	} else {
		selected = strings.Join(lines[startLine:], "\n")
	}

	truncation := tool.TruncateHead(selected, tool.Limits{})

	var output string
	var details *readDetails
	switch {
	case truncation.FirstLineExceedsLimit:
		// A single line larger than the whole byte budget cannot be shown
		// without breaking it, so hand the model a command that can.
		firstLineSize := tool.FormatSize(len(lines[startLine]))
		output = fmt.Sprintf(
			"[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay, firstLineSize, tool.FormatSize(tool.DefaultMaxBytes),
			startLineDisplay, request.Path, tool.DefaultMaxBytes,
		)
		details = &readDetails{Truncation: &truncation}
	case truncation.Truncated:
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		nextOffset := endLineDisplay + 1
		output = truncation.Content
		if truncation.TruncatedBy == "lines" {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines,
				tool.FormatSize(tool.DefaultMaxBytes), nextOffset)
		}
		details = &readDetails{Truncation: &truncation}
	case userLimited && startLine+userLimitedLines < len(lines):
		remaining := len(lines) - (startLine + userLimitedLines)
		nextOffset := startLine + userLimitedLines + 1
		output = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]",
			truncation.Content, remaining, nextOffset)
	default:
		output = truncation.Content
	}

	structured, err := json.Marshal(output)
	if err != nil {
		structured = json.RawMessage(`""`)
	}
	result := tool.Result{
		Content:           []wire.Content{wire.TextContent(output)},
		StructuredContent: structured,
	}
	if details != nil {
		detailsJSON, err := json.Marshal(details)
		if err != nil {
			return tool.Result{}, err
		}
		result.Details = detailsJSON
	}
	return result, nil
}

// headOf returns at most imgSniffBytes of data for MIME detection.
func headOf(data []byte) []byte {
	if len(data) <= imgSniffBytes {
		return data
	}
	return data[:imgSniffBytes]
}

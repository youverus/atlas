package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const writeDescription = "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. " +
	"Automatically creates parent directories."

const writeParameters = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path to the file to write (relative or absolute)"},
    "content": {"type": "string", "description": "Content to write to the file"}
  },
  "required": ["path", "content"]
}`

type writeTool struct{ set *shared }

func (t writeTool) Name() string                { return "write" }
func (t writeTool) Label() string               { return "write" }
func (t writeTool) Description() string         { return writeDescription }
func (t writeTool) Parameters() json.RawMessage { return json.RawMessage(writeParameters) }

type writeRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t writeTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request writeRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if request.Path == "" {
		return tool.Result{}, fmt.Errorf("write: path is required")
	}

	absolute := t.set.resolve(request.Path)
	err := t.set.queue.with(absolute, func() error {
		// Checked inside the lock so an abort cannot release the queue while a
		// write is still in flight.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.WriteFile(absolute, []byte(request.Content), 0o644)
	})
	if err != nil {
		return tool.Result{}, err
	}

	return tool.Result{
		Content: []wire.Content{wire.TextContent(fmt.Sprintf("Successfully wrote to %s", request.Path))},
	}, nil
}

// Package echo is a minimal tool used by tests and the demo CLI. It returns the
// text it receives, which makes loop and dispatch behavior observable without
// touching the filesystem.
package echo

import (
	"context"
	"encoding/json"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

var parameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "Text to echo back"}
  },
  "required": ["text"],
  "additionalProperties": false
}`)

// Tool implements tool.Tool.
type Tool struct{}

// New creates the echo tool.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "echo" }

// Label implements tool.Tool.
func (*Tool) Label() string { return "Echo" }

// Description implements tool.Tool.
func (*Tool) Description() string {
	return "Return the provided text unchanged. Useful for verifying tool wiring."
}

// Parameters implements tool.Tool.
func (*Tool) Parameters() json.RawMessage { return parameters }

// Execute implements tool.Tool.
func (*Tool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args struct {
		Text string `json:"text"`
	}
	if err := tool.Decode(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Text == "" {
		return tool.Result{
			Content: []wire.Content{wire.TextContent("echo: missing required argument \"text\"")},
			IsError: true,
		}, nil
	}
	details, _ := json.Marshal(map[string]any{"length": len([]rune(args.Text))})
	return tool.Result{
		Content: []wire.Content{wire.TextContent("echo: " + args.Text)},
		Details: details,
	}, nil
}

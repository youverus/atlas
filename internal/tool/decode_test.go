package tool_test

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/tool"
)

func TestUnwrap(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "object", in: `{"path":"a.txt"}`, want: `{"path":"a.txt"}`},
		{name: "string wrapped", in: `"{\"path\":\"a.txt\"}"`, want: `{"path":"a.txt"}`},
		{name: "code fence", in: "```json\n{\"path\":\"a.txt\"}\n```", want: `{"path":"a.txt"}`},
		{name: "empty", in: ``, want: `{}`},
		{name: "null", in: `null`, want: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tool.Unwrap(json.RawMessage(test.in))
			if err != nil {
				t.Fatalf("unwrap: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestUnwrapRejectsInvalid(t *testing.T) {
	if _, err := tool.Unwrap(json.RawMessage(`{"path":`)); err == nil {
		t.Fatal("expected an error for truncated JSON")
	}
	if _, err := tool.Unwrap(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("expected an error for a non-object")
	}
}

func TestDecodeNumberLeniency(t *testing.T) {
	var args struct {
		Limit  tool.Number `json:"limit"`
		Offset tool.Number `json:"offset"`
	}
	raw := json.RawMessage(`{"limit":"10","offset":5.0}`)
	if err := tool.Decode(raw, &args); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if args.Limit.Int() != 10 || args.Offset.Int() != 5 {
		t.Fatalf("got limit=%d offset=%d", args.Limit, args.Offset)
	}
}

func TestDecodeReportsField(t *testing.T) {
	var args struct {
		Path string `json:"path"`
	}
	err := tool.Decode(json.RawMessage(`{"path":42}`), &args)
	if err == nil {
		t.Fatal("expected a type error")
	}
	if !strings.Contains(err.Error(), "decode arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

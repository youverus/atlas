package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrNoArguments is returned when a call carried no arguments and the tool
// requires them. Decoding an empty payload as {} succeeds, so tools that need
// fields report the missing field themselves.
var ErrNoArguments = errors.New("tool: no arguments")

// Unwrap normalizes raw tool arguments. Models sometimes send the argument
// object as a JSON string, or wrap it in a code fence. Both are recovered here
// so tool code sees a plain object.
func Unwrap(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	if trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err != nil {
			return nil, fmt.Errorf("tool: arguments string: %w", err)
		}
		trimmed = bytes.TrimSpace([]byte(inner))
		if len(trimmed) == 0 {
			return json.RawMessage("{}"), nil
		}
	}
	trimmed = stripCodeFence(trimmed)
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("tool: arguments are not valid JSON: %.200s", trimmed)
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("tool: arguments must be a JSON object, got %.200s", trimmed)
	}
	return json.RawMessage(trimmed), nil
}

// Decode unwraps and decodes arguments into dst. Numbers stay json.Number so
// tools can distinguish integers from strings.
func Decode(raw json.RawMessage, dst any) error {
	normalized, err := Unwrap(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("tool: decode arguments: %w", err)
	}
	return nil
}

// Number is an int that accepts a JSON string, a number, or a float with no
// fractional part. Models frequently send "10" where 10 is declared.
type Number int

// UnmarshalJSON implements json.Unmarshaler.
func (n *Number) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*n = 0
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		trimmed = bytes.TrimSpace([]byte(text))
	}
	if value, err := strconv.ParseInt(string(trimmed), 10, 64); err == nil {
		*n = Number(value)
		return nil
	}
	value, err := strconv.ParseFloat(string(trimmed), 64)
	if err != nil {
		return fmt.Errorf("tool: expected an integer, got %.60s", data)
	}
	*n = Number(int(value))
	return nil
}

// Int returns the value as int.
func (n Number) Int() int { return int(n) }

func stripCodeFence(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte("```")) {
		return data
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) < 2 {
		return data
	}
	// Drop the opening fence (and optional language tag) and a closing fence.
	lines = lines[1:]
	if last := bytes.TrimSpace(lines[len(lines)-1]); bytes.HasPrefix(last, []byte("```")) {
		lines = lines[:len(lines)-1]
	}
	return bytes.TrimSpace(bytes.Join(lines, []byte("\n")))
}

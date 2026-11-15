package responses_test

import (
	"context"
	"encoding/json"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/responses"
	"pi-go/internal/wire"
)

// captureCompat sends one request through a provider built from compat and
// returns the decoded body that reached the server.
func captureCompat(t *testing.T, compat responses.Compat, request provider.Request) map[string]any {
	t.Helper()
	server := sseServer(t, "text.sse")
	compat.BaseURL = server.URL + "/v1"
	request.Model.BaseURL = ""

	stream, err := responses.New(compat).Stream(context.Background(), request)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, _, err := provider.Collect(stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var payload map[string]any
	body := server.last(t).body
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request: %v\n%s", err, body)
	}
	return payload
}

// toolRequestModel declares one tool so tool_choice has something to point at.
func toolRequestModel() provider.Request {
	head := wire.SystemMessage("You are a test agent.")
	head.ToolsAdded = []wire.Tool{{
		Name:        "read",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}}
	return provider.Request{
		Model:    wire.Model{ID: "gpt-x", API: responses.APIName, Provider: "openai"},
		Messages: []wire.Message{head, wire.UserMessage("hi")},
	}
}

// TestToolChoiceTravels pins the shared vocabulary, which this API spells the
// same way the chat completions endpoint does. An unknown value sends nothing
// rather than a string the server rejects.
func TestToolChoiceTravels(t *testing.T) {
	cases := []struct {
		value string
		want  string // empty means the field is absent
	}{
		{value: ""},
		{value: "auto", want: "auto"},
		{value: "none", want: "none"},
		{value: "required", want: "required"},
		{value: "bogus"},
	}

	for _, test := range cases {
		t.Run("value="+test.value, func(t *testing.T) {
			compat := responses.Compat{ToolChoice: test.value}
			payload := captureCompat(t, compat, toolRequestModel())

			got, present := payload["tool_choice"]
			if test.want == "" {
				if present {
					t.Fatalf("tool_choice present: %#v", got)
				}
				return
			}
			if got != test.want {
				t.Fatalf("tool_choice: got %#v want %q", got, test.want)
			}
		})
	}
}

// TestToolChoiceNeedsTools keeps the field from travelling alone: a request
// without declarations has nothing to choose from.
func TestToolChoiceNeedsTools(t *testing.T) {
	compat := responses.Compat{ToolChoice: "required"}
	payload := captureCompat(t, compat, textRequest(wire.Model{}, "hi"))

	if got, present := payload["tool_choice"]; present {
		t.Fatalf("tool_choice present without tools: %#v", got)
	}
}

// TestDeploymentKnobsTravelTogether pins the three fields a compat table can
// set independently, and that an empty one leaves the request as it was.
func TestDeploymentKnobsTravelTogether(t *testing.T) {
	compat := responses.Compat{
		ServiceTier: "flex",
		Verbosity:   "low",
		Truncation:  "auto",
	}
	payload := captureCompat(t, compat, textRequest(wire.Model{}, "hi"))

	if got := payload["service_tier"]; got != "flex" {
		t.Fatalf("service_tier: got %#v want flex", got)
	}
	if got := payload["truncation"]; got != "auto" {
		t.Fatalf("truncation: got %#v want auto", got)
	}
	text, ok := payload["text"].(map[string]any)
	if !ok {
		t.Fatalf("text is not an object: %#v", payload["text"])
	}
	if got := text["verbosity"]; got != "low" {
		t.Fatalf("text.verbosity: got %#v want low", got)
	}
}

// TestDeploymentKnobsStayHome pins the other half: without a compat table none
// of the three keys appears, because an unknown one can make a gateway refuse
// the request.
func TestDeploymentKnobsStayHome(t *testing.T) {
	payload := captureCompat(t, responses.Compat{}, textRequest(wire.Model{}, "hi"))

	for _, key := range []string{"service_tier", "truncation", "text"} {
		if got, present := payload[key]; present {
			t.Fatalf("%s present: %#v", key, got)
		}
	}
}

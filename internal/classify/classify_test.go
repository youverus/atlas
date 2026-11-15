package classify_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pi-go/internal/classify"
	"pi-go/internal/permission"
	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

func asking() permission.ClassifyRequest {
	return permission.ClassifyRequest{
		Tool:   "bash",
		Args:   `{"command":"npm test"}`,
		Reason: "no permission rule matched bash; approval is required",
	}
}

func classifier(t *testing.T, answers ...faux.Response) *classify.Classifier {
	t.Helper()
	return &classify.Classifier{
		Provider: faux.New(answers...),
		Model:    wire.Model{ID: "model", MaxTokens: 4096},
	}
}

func TestAllowIsAVerdict(t *testing.T) {
	script := faux.New(faux.Response{Text: `{"verdict":"allow","reason":"running the test suite"}`})
	c := &classify.Classifier{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 4096}}

	verdict, err := c.Classify(context.Background(), asking())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict.Behavior != permission.Allow {
		t.Fatalf("behavior: got %q want allow", verdict.Behavior)
	}
	if verdict.Reason != "running the test suite" {
		t.Fatalf("reason: got %q", verdict.Reason)
	}
	if calls := script.Calls(); len(calls) != 1 {
		t.Fatalf("calls: got %d want 1", len(calls))
	}
}

func TestTheQuestionCarriesTheCallAndNotTheConversation(t *testing.T) {
	script := faux.New(faux.Response{Text: `{"verdict":"allow"}`})
	c := &classify.Classifier{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 4096}}
	if _, err := c.Classify(context.Background(), asking()); err != nil {
		t.Fatalf("classify: %v", err)
	}

	request := script.Calls()[0]
	if len(request.Messages) != 2 {
		t.Fatalf("messages: got %d want a system prompt and one question", len(request.Messages))
	}
	if request.Messages[0].Role != wire.RoleSystem {
		t.Fatalf("first message: got %q want system", request.Messages[0].Role)
	}
	if !strings.Contains(request.Messages[0].Content.Text(), "permission classifier") {
		t.Fatalf("system prompt: %q", request.Messages[0].Content.Text())
	}
	question := request.Messages[1].Content.Text()
	if !strings.Contains(question, "tool: bash") {
		t.Fatalf("question tool: %q", question)
	}
	if !strings.Contains(question, `{"command":"npm test"}`) {
		t.Fatalf("question arguments: %q", question)
	}
	if !strings.Contains(question, "<ladder-reason>") {
		t.Fatalf("question reason: %q", question)
	}
	if request.Options.MaxTokens == nil || *request.Options.MaxTokens != 300 {
		t.Fatalf("maxTokens: got %v want 300", request.Options.MaxTokens)
	}
}

func TestDenyIsAVerdict(t *testing.T) {
	verdict, err := classifier(t, faux.Response{
		Text: `{"verdict":"deny","reason":"this formats the disk"}`,
	}).Classify(context.Background(), asking())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict.Behavior != permission.Deny {
		t.Fatalf("behavior: got %q want deny", verdict.Behavior)
	}
	if verdict.Reason != "this formats the disk" {
		t.Fatalf("reason: got %q", verdict.Reason)
	}
}

func TestAFencedAnswerStillReads(t *testing.T) {
	verdict, err := classifier(t, faux.Response{
		Text: "```json\n{\"verdict\":\"allow\",\"reason\":\"ok\"}\n```",
	}).Classify(context.Background(), asking())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict.Behavior != permission.Allow {
		t.Fatalf("behavior: got %q want allow", verdict.Behavior)
	}
}

func TestATrailingSentenceDoesNotChangeTheVerdict(t *testing.T) {
	verdict, err := classifier(t, faux.Response{
		Text: `{"verdict":"deny","reason":"wipes the volume"} This is my final answer.`,
	}).Classify(context.Background(), asking())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict.Behavior != permission.Deny {
		t.Fatalf("behavior: got %q want deny", verdict.Behavior)
	}
}

// Every test below is the same contract from a different angle: the classifier
// cannot settle the ask, so it must report an error and let internal/permission put
// the question in front of the user. None of them may return allow.
func TestAnUnusableAnswerIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer faux.Response
		want   string
	}{
		{
			name:   "prose",
			answer: faux.Response{Text: "I think this is probably fine to run."},
			want:   "not a JSON object",
		},
		{
			name:   "confirm is not a verdict",
			answer: faux.Response{Text: `{"verdict":"confirm","reason":"unsure"}`},
			want:   "only allow and deny",
		},
		{
			name:   "no verdict field",
			answer: faux.Response{Text: `{"reason":"looks fine"}`},
			want:   "no verdict",
		},
		{
			name:   "empty body",
			answer: faux.Response{},
			want:   "no text",
		},
		{
			name:   "malformed object",
			answer: faux.Response{Text: `{"verdict": }`},
			want:   "not a JSON object",
		},
		{
			name:   "provider error",
			answer: faux.Response{Err: errString("upstream is down")},
			want:   "upstream is down",
		},
		{
			name:   "length stop",
			answer: faux.Response{Text: `{"verdict":"allow"`, StopReason: wire.StopLength},
			want:   "token cap",
		},
		{
			name: "a tool call is not an answer",
			answer: faux.Response{
				Text:      "",
				ToolCalls: []wire.Content{wire.ToolCallContent("id", "classify", json.RawMessage(`{}`))},
			},
			want: "called a tool",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict, err := classifier(t, tc.answer).Classify(context.Background(), asking())
			if err == nil {
				t.Fatalf("got verdict %+v, want an error so the human is asked", verdict)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if verdict.Behavior != permission.Undecided {
				t.Fatalf("behavior: got %q want undecided", verdict.Behavior)
			}
		})
	}
}

func TestNoProviderIsAnError(t *testing.T) {
	c := &classify.Classifier{Model: wire.Model{ID: "model"}}
	if _, err := c.Classify(context.Background(), asking()); err == nil {
		t.Fatal("a classifier with no provider must not answer")
	}
}

func TestTheTimeoutBoundsTheCall(t *testing.T) {
	c := &classify.Classifier{
		Provider: faux.New(faux.Response{Text: `{"verdict":"allow"}`}),
		Model:    wire.Model{ID: "model"},
		Timeout:  time.Nanosecond,
	}
	// The faux provider checks the context before it streams, so a deadline that
	// already passed surfaces as an error rather than a verdict.
	time.Sleep(time.Millisecond)
	if _, err := c.Classify(context.Background(), asking()); err == nil {
		t.Fatal("an expired deadline must be an error, not a verdict")
	}
}

// errString is an error that survives being written in a table.
type errString string

func (e errString) Error() string { return string(e) }

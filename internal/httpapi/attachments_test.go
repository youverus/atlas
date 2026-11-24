package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"pi-go/internal/wire"
)

func TestAskAcceptsAttachmentsAndForwardsPayload(t *testing.T) {
	gotCh := make(chan []Attachment, 1)
	srv := New(Options{
		Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
			return Receipt{}, nil
		},
		SessionAttachmentTurn: func(_ context.Context, _ string, _ string, attachments []Attachment, _ func(wire.AgentEvent) error) (Receipt, error) {
			gotCh <- attachments
			return Receipt{}, nil
		},
	})
	if _, err := srv.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"prompt": "",
		"attachments": []Attachment{
			{Kind: "text", Name: "hello.txt", Data: "world"},
		},
	})
	resp, err := http.Post(srv.URL()+"/api/ask", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post /api/ask: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	got := <-gotCh
	if len(got) != 1 || got[0].Kind != "text" || got[0].Name != "hello.txt" {
		t.Fatalf("unexpected forwarded attachments: %+v", got)
	}
}

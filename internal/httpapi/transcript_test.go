package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// transcriptHost answers only the replay question; every other method of the
// interface is left to the embedded nil.
type transcriptHost struct {
	Host
	blocks []TranscriptBlock
	err    error
}

func (h *transcriptHost) Transcript() ([]TranscriptBlock, error) {
	return h.blocks, h.err
}

// A window that switches to a conversation asks a second question: the switch
// itself only moved the kernel, so without this route the page kept whatever
// the last turn had drawn, which looks like the history was thrown away.
func TestServeSessionTranscriptReplaysTheConversation(t *testing.T) {
	host := &transcriptHost{blocks: []TranscriptBlock{
		{Role: "user", Text: "why"},
		{Role: "tool", Name: "read", Note: "done", ID: "c1", Args: "{\n  \"path\": \"a.go\"\n}", Result: "package a"},
	}}
	server := projectServer(t, host, nil)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/sessions/transcript")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var blocks []TranscriptBlock
	if err := json.NewDecoder(response.Body).Decode(&blocks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(blocks) != 2 || blocks[0].Role != "user" || blocks[1].Name != "read" {
		t.Fatalf("blocks = %+v, want the replayed conversation", blocks)
	}
	// A card that opens needs the call itself on the wire, not just the line:
	// without these two a replayed tool card has nothing to show.
	if blocks[1].Args != "{\n  \"path\": \"a.go\"\n}" || blocks[1].Result != "package a" {
		t.Fatalf("tool block = %+v, want the arguments and the result carried through", blocks[1])
	}
}

// An empty conversation is a list, not null: the page folds the answer and a
// null would make it branch on a shape the server is responsible for.
func TestServeSessionTranscriptAnswersAnEmptyList(t *testing.T) {
	server := projectServer(t, &transcriptHost{}, nil)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/sessions/transcript")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	body := make([]byte, 8)
	n, _ := response.Body.Read(body)
	if got := string(body[:n]); got != "[]\n" && got != "[]" {
		t.Fatalf("body = %q, want an empty list", got)
	}
}

package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

// readAll is the refusal text, which is where the kernel's own wording reaches
// the reader.
func readAll(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

// forkHost records what the route passed down. The embedded interface stands in
// for the rest of Host, so a route that reached another method would panic — the
// right answer for a test about these two calls.
type forkHost struct {
	Host

	points  []ForkPoint
	entries []string
	names   []string
	text    string
	err     error
}

func (h *forkHost) ForkPoints() []ForkPoint { return h.points }

func (h *forkHost) ForkSession(entryID, name string) (string, error) {
	if h.err != nil {
		return "", h.err
	}
	h.entries = append(h.entries, entryID)
	h.names = append(h.names, name)
	return h.text, nil
}

// TestForkRoutePassesTheName covers both shapes the page sends: a name the
// reader typed, and no body at all when the copy should keep its inherited name.
func TestForkRoutePassesTheName(t *testing.T) {
	host := &forkHost{}
	server := projectServer(t, host, nil)

	if response := postJSON(t, server.URL+"/api/sessions/fork", `{"name":"plan B"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("named fork status = %d", response.StatusCode)
	}
	if response := postJSON(t, server.URL+"/api/sessions/fork", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("bare fork status = %d", response.StatusCode)
	}
	if len(host.names) != 2 {
		t.Fatalf("forks = %d, want 2", len(host.names))
	}
	if host.names[0] != "plan B" {
		t.Fatalf("first name = %q, want %q", host.names[0], "plan B")
	}
	if host.names[1] != "" {
		t.Fatalf("second name = %q, want empty", host.names[1])
	}
	if host.entries[1] != "" {
		t.Fatalf("second entry = %q, want the end of the conversation", host.entries[1])
	}
}

// TestForkRoutePassesTheEntryAndReturnsTheDraft is the point of the change: the
// page names where to branch, and the kernel's answer carries the message that
// was left behind so the input box can offer it back.
func TestForkRoutePassesTheEntryAndReturnsTheDraft(t *testing.T) {
	host := &forkHost{text: "second question"}
	server := projectServer(t, host, nil)

	response := postJSON(t, server.URL+"/api/sessions/fork", `{"entryId":"e-2","name":"plan B"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if len(host.entries) != 1 || host.entries[0] != "e-2" {
		t.Fatalf("entries = %v, want the one the page named", host.entries)
	}
	var body forkSessionResponse
	if err := json.Unmarshal([]byte(readAll(t, response)), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Text != "second question" {
		t.Fatalf("body = %+v, want ok with the draft", body)
	}
}

// TestForkPointsRouteAnswersTheList keeps the empty list from arriving as null:
// the page maps over the answer, and null is not a list it can map.
func TestForkPointsRouteAnswersTheList(t *testing.T) {
	host := &forkHost{points: []ForkPoint{
		{Index: 0},
		{EntryID: "e-1", Index: 1, Text: "hi"},
	}}
	server := projectServer(t, host, nil)

	response, err := http.Get(server.URL + "/api/sessions/fork-points")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var points []ForkPoint
	if err := json.Unmarshal([]byte(readAll(t, response)), &points); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(points) != 2 || points[1].EntryID != "e-1" || points[1].Index != 1 {
		t.Fatalf("points = %+v, want the two the host answered", points)
	}

	empty := projectServer(t, &forkHost{}, nil)
	answer, err := http.Get(empty.URL + "/api/sessions/fork-points")
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if got := readAll(t, answer); got != "[]\n" {
		t.Fatalf("empty answer = %q, want an empty list", got)
	}
}

// TestForkRouteReportsWhatTheKernelRefused keeps the kernel's own words on the
// way out: "nothing to fork" is a state the reader can act on (say something
// first), and a generic failure would hide it.
func TestForkRouteReportsWhatTheKernelRefused(t *testing.T) {
	host := &forkHost{err: errors.New("app: nothing to fork (session is empty)")}
	server := projectServer(t, host, nil)

	response := postJSON(t, server.URL+"/api/sessions/fork", `{"name":"plan B"}`)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.StatusCode)
	}
	body := readAll(t, response)
	if body != "app: nothing to fork (session is empty)\n" {
		t.Fatalf("body = %q, want the kernel's message", body)
	}
}

// TestForkRouteRejectsABadBody keeps a malformed body from reaching the kernel
// as an unnamed fork.
func TestForkRouteRejectsABadBody(t *testing.T) {
	host := &forkHost{}
	server := projectServer(t, host, nil)

	if response := postJSON(t, server.URL+"/api/sessions/fork", `{"name":`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if len(host.names) != 0 {
		t.Fatalf("the kernel was asked to fork %d times", len(host.names))
	}
}

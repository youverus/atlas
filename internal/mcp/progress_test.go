package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pi-go/internal/tool"
)

// TestProgressKeepsCallAlive is the whole point of the idle budget: five ticks of
// 25ms outlast a 120ms timeout, so a client that treats the number as a total
// would hang up in the middle of a working call.
func TestProgressKeepsCallAlive(t *testing.T) {
	spec := fakeSpec(t, "progress")
	spec.Timeout = 120 * time.Millisecond
	set := Open(context.Background(), []Spec{spec}, Identity{Name: "atlas", Version: "0.9.0"})
	t.Cleanup(set.Close)
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}

	var mu sync.Mutex
	var updates []string
	proxy := findTool(t, set, "mcp__fake__read_file")
	if _, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(partial tool.Partial) {
		mu.Lock()
		defer mu.Unlock()
		for _, content := range partial.Content {
			updates = append(updates, content.Text)
		}
	}); err != nil {
		t.Fatalf("call: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 5 {
		t.Fatalf("updates = %v, want one per notification", updates)
	}
	if updates[0] != "step 1 of 5" {
		t.Fatalf("first update = %q, want the server's own message", updates[0])
	}
}

// TestSilenceExpiresTheCall is the other half: a server that says nothing is cut
// off, and the error names the budget, the silence and the setting to change.
func TestSilenceExpiresTheCall(t *testing.T) {
	spec := fakeSpec(t, "slow")
	spec.Timeout = 80 * time.Millisecond
	set := Open(context.Background(), []Spec{spec}, Identity{Name: "atlas", Version: "0.9.0"})
	t.Cleanup(set.Close)
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}

	proxy := findTool(t, set, "mcp__fake__read_file")
	_, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, nil)
	if err == nil {
		t.Fatal("call succeeded, want the idle budget to end it")
	}
	for _, want := range []string{"silent for 80ms", `"timeout"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %s", err, want)
		}
	}
}

// TestProgressFromTheAnswerStream proves the POST body is read as it arrives: the
// notifications and the answer share one stream, and progress that was only
// collected at the end would not have kept anything alive.
func TestProgressFromTheAnswerStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var message incoming
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch message.Method {
		case "initialize":
			writeJSON(w, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "chatty", "version": "1"},
			})
		case "tools/list":
			writeJSON(w, message.ID, map[string]any{"tools": []any{
				map[string]any{"name": "work", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			// One stream, both kinds of message, notifications first.
			w.Header().Set("Content-Type", "text/event-stream")
			echo := progressTokenOf(message.Params)
			for step := 1; step <= 5; step++ {
				writeEvent(w, map[string]any{
					"jsonrpc": jsonrpcVersion,
					"method":  methodProgress,
					"params":  progressParams(echo, step, 5),
				})
				w.(http.Flusher).Flush()
				time.Sleep(progressTick)
			}
			writeEvent(w, response(*message.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "done"}},
			}))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(server.Close)

	set := Open(context.Background(), []Spec{{
		Name: "remote", URL: server.URL, Timeout: 120 * time.Millisecond,
	}}, Identity{Name: "atlas", Version: "0.9.0"})
	t.Cleanup(set.Close)
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}

	var mu sync.Mutex
	updates := 0
	proxy := findTool(t, set, "mcp__remote__work")
	result, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(tool.Partial) {
		mu.Lock()
		defer mu.Unlock()
		updates++
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := result.Content[0].Text; got != "done" {
		t.Fatalf("result = %q, want the answer that came after the notifications", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if updates != 5 {
		t.Fatalf("updates = %d, want one per notification on the answer stream", updates)
	}
}

// TestProgressFromTheGetStream is the channel this client did not have: the tool
// answers nothing for 300ms, so the only thing that can keep it alive is the GET
// side of the session.
func TestProgressFromTheGetStream(t *testing.T) {
	var mu sync.Mutex
	var token any
	gets := 0
	opened := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			gets++
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-opened:
			case <-r.Context().Done():
				return
			}
			mu.Lock()
			echo := token
			mu.Unlock()
			for step := 1; step <= 10; step++ {
				writeEvent(w, map[string]any{
					"jsonrpc": jsonrpcVersion,
					"method":  methodProgress,
					"params":  progressParams(echo, step, 10),
				})
				w.(http.Flusher).Flush()
				select {
				case <-time.After(progressTick):
				case <-r.Context().Done():
					return
				}
			}
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var message incoming
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch message.Method {
		case "initialize":
			writeJSON(w, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "slow", "version": "1"},
			})
		case "tools/list":
			writeJSON(w, message.ID, map[string]any{"tools": []any{
				map[string]any{"name": "build", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			mu.Lock()
			token = progressTokenOf(message.Params)
			mu.Unlock()
			close(opened)
			// Longer than the budget three times over: only the GET keeps this
			// call from being abandoned.
			time.Sleep(300 * time.Millisecond)
			writeJSON(w, message.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "built"}},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(server.Close)

	set := Open(context.Background(), []Spec{{
		Name: "remote", URL: server.URL, Timeout: 120 * time.Millisecond,
	}}, Identity{Name: "atlas", Version: "0.9.0"})
	t.Cleanup(set.Close)
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}

	proxy := findTool(t, set, "mcp__remote__build")
	result, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatalf("call: %v (progress on the GET stream had to keep it alive)", err)
	}
	if got := result.Content[0].Text; got != "built" {
		t.Fatalf("result = %q, want built", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if gets == 0 {
		t.Fatal("the server saw no GET, want the stream channel opened")
	}
}

// TestHTTPWithoutStreamStillCallsTools is the compatibility half: a server that
// does not implement GET answers 405, and that must cost the session nothing.
func TestHTTPWithoutStreamStillCallsTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var message incoming
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch message.Method {
		case "initialize":
			writeJSON(w, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "plain", "version": "1"},
			})
		case "tools/list":
			writeJSON(w, message.ID, map[string]any{"tools": []any{
				map[string]any{"name": "ping", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			writeJSON(w, message.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "pong"}},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(server.Close)

	set := Open(context.Background(), []Spec{{Name: "remote", URL: server.URL}}, Identity{Name: "atlas", Version: "0.9.0"})
	t.Cleanup(set.Close)
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}

	proxy := findTool(t, set, "mcp__remote__ping")
	result, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := result.Content[0].Text; got != "pong" {
		t.Fatalf("result = %q, want pong", got)
	}
}

// TestGetStreamDropsWhatItDoesNotUnderstand keeps the other notifications from
// becoming a failure: a tool list that changed cannot be acted on (the table is
// fixed when the session starts), a log record has nowhere to go, and neither may
// take the session down.
func TestGetStreamDropsWhatItDoesNotUnderstand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeEvent(w, map[string]any{"jsonrpc": jsonrpcVersion, "method": "notifications/tools/list_changed"})
			writeEvent(w, map[string]any{"jsonrpc": jsonrpcVersion, "method": "notifications/message", "params": map[string]any{"level": "info", "data": "hello"}})
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var message incoming
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch message.Method {
		case "initialize":
			writeJSON(w, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "chatty", "version": "1"},
			})
		case "tools/list":
			writeJSON(w, message.ID, map[string]any{"tools": []any{
				map[string]any{"name": "ping", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			writeJSON(w, message.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "pong"}},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(server.Close)

	set := Open(context.Background(), []Spec{{Name: "remote", URL: server.URL}}, Identity{Name: "atlas", Version: "0.9.0"})
	t.Cleanup(set.Close)
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := len(set.Servers()[0].Tools); got != 1 {
		t.Fatalf("tools = %d, want the one the handshake listed", got)
	}

	// Give the stream a moment to deliver both notifications, then prove the
	// session still answers.
	time.Sleep(2 * progressTick)
	proxy := findTool(t, set, "mcp__remote__ping")
	if _, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
}

// TestProgressTextPrefersTheServerMessage covers the three shapes one
// notification can arrive in.
func TestProgressTextPrefersTheServerMessage(t *testing.T) {
	cases := []struct {
		note progressNote
		want string
	}{
		{progressNote{Progress: 3, Total: 10, Message: "indexing"}, "indexing"},
		{progressNote{Progress: 3, Total: 10}, "3/10"},
		{progressNote{Progress: 3}, "3"},
	}
	for _, test := range cases {
		if got := progressText(test.note); got != test.want {
			t.Fatalf("progressText(%+v) = %q, want %q", test.note, got, test.want)
		}
	}
}

// TestProgressWithoutACallIsDropped is the late notification: a call that has
// already given up is gone from the map, and a token nobody issued may arrive as
// a string or as a number. Neither is an error, and neither may panic.
func TestProgressWithoutACallIsDropped(t *testing.T) {
	c := &client{tr: &stdioTransport{}, calls: map[string]*callState{}}
	for _, params := range []string{
		`{"progressToken":"99","progress":1}`,
		`{"progressToken":7,"progress":1}`,
		`{"progressToken":null}`,
		`{}`,
		`not json`,
	} {
		c.notify(incoming{Method: methodProgress, Params: json.RawMessage(params)})
	}
	c.notify(incoming{Method: "notifications/message", Params: json.RawMessage(`{}`)})
}

// progressParams is one notifications/progress payload, echoing the token exactly
// as it was received.
func progressParams(token any, step, total int) map[string]any {
	return map[string]any{
		"progressToken": token,
		"progress":      step,
		"total":         total,
		"message":       fmt.Sprintf("step %d of %d", step, total),
	}
}

// progressTokenOf reads the token this client put on a tools/call.
func progressTokenOf(params json.RawMessage) any {
	var shape struct {
		Meta struct {
			ProgressToken any `json:"progressToken"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(params, &shape)
	return shape.Meta.ProgressToken
}

// writeEvent writes one server-sent event with a single data line.
func writeEvent(w http.ResponseWriter, message any) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", encoded)
}

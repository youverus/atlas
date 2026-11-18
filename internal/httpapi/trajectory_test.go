package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"pi-go/internal/wire"
)

// trajectoryHost answers only the fold question; every other method of the
// interface is left to the embedded nil.
type trajectoryHost struct {
	Host
	rows []TrajectoryRow
	err  error
}

func (h *trajectoryHost) Trajectory() ([]TrajectoryRow, error) {
	return h.rows, h.err
}

// The window asks for the recorded facts of the conversation in view, which is
// the question the transcript cannot answer: it keeps the thinking, the usage
// and the compactions, and it keeps a tool's arguments and output whole.
func TestServeSessionTrajectoryReportsTheRows(t *testing.T) {
	host := &trajectoryHost{rows: []TrajectoryRow{
		{Index: 0, Seq: 2, EntryID: "e1", Turn: 1, Kind: "prompt", Timestamp: "2026-11-18T12:00:01.000Z", Text: "why"},
		{
			Index: 1, Seq: 3, EntryID: "e2", ParentID: "e1", Turn: 1, Kind: "tool",
			Timestamp: "2026-11-18T12:00:02.000Z", DurationMs: 37, IsError: true,
			Tool: "read", Ref: "c1", Args: `{"path":"a.go"}`,
			Result: "permission denied: outside the workspace",
		},
	}}
	server := projectServer(t, host, nil)

	response, err := http.Get(server.URL + "/api/sessions/trajectory")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var rows []TrajectoryRow
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// A row that lost the call's outcome would draw a card with nothing behind
	// it, so the arguments, the denial and its duration have to survive the wire.
	call := rows[1]
	if call.Tool != "read" || call.Args != `{"path":"a.go"}` {
		t.Fatalf("call row = %+v, want the call's arguments", call)
	}
	if !call.IsError || call.Result == "" || call.DurationMs != 37 {
		t.Fatalf("call row = %+v, want the failed outcome with its duration", call)
	}
}

// An empty conversation is a list, not null: the page folds the answer and a null
// would make it branch on a shape the server is responsible for.
func TestServeSessionTrajectoryAnswersAnEmptyList(t *testing.T) {
	server := projectServer(t, &trajectoryHost{}, nil)

	response, err := http.Get(server.URL + "/api/sessions/trajectory")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	var rows []TrajectoryRow
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rows == nil {
		t.Fatal("body decoded to null, want an empty list")
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// A session that cannot be folded is reported as a conflict rather than as an
// empty view: an empty list would look like a conversation with no history.
func TestServeSessionTrajectoryReportsAFailure(t *testing.T) {
	server := projectServer(t, &trajectoryHost{err: errors.New("no leaf")}, nil)

	response, err := http.Get(server.URL + "/api/sessions/trajectory")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.StatusCode)
	}
}

// The usage a round was billed for is part of the row, down to the cache
// counters: the reason to open this view is usually a cost or a context
// question, and a zero here would be indistinguishable from "not recorded".
func TestTrajectoryRowCarriesUsage(t *testing.T) {
	host := &trajectoryHost{rows: []TrajectoryRow{{
		Index: 0, Seq: 3, EntryID: "e2", Turn: 1, Kind: "assistant",
		Usage: &wire.Usage{Input: 1200, Output: 40, CacheRead: 900, TotalTokens: 1240},
	}}}
	server := projectServer(t, host, nil)

	response, err := http.Get(server.URL + "/api/sessions/trajectory")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	var rows []TrajectoryRow
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Usage == nil {
		t.Fatalf("rows = %+v, want the recorded usage", rows)
	}
	if rows[0].Usage.CacheRead != 900 || rows[0].Usage.TotalTokens != 1240 {
		t.Fatalf("usage = %+v, want the cache counters", rows[0].Usage)
	}
}

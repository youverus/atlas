package httpapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pi-go/internal/wire"
)

type stubTerminalController struct {
	createdDir string
	cols       int
	rows       int
	writes     [][]byte
	resizes    [][2]int
	closedIDs  []string
	sub        TerminalSubscription
	createErr  error
	subErr     error
}

func (s *stubTerminalController) CreateTerminal(dir string, cols, rows int) (TerminalSessionInfo, error) {
	if s.createErr != nil {
		return TerminalSessionInfo{}, s.createErr
	}
	s.createdDir = dir
	s.cols = cols
	s.rows = rows
	return TerminalSessionInfo{ID: "term-1", Shell: "/bin/zsh", CWD: dir}, nil
}

func (s *stubTerminalController) WriteTerminal(id string, data []byte) error {
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}

func (s *stubTerminalController) ResizeTerminal(id string, cols, rows int) error {
	s.resizes = append(s.resizes, [2]int{cols, rows})
	return nil
}

func (s *stubTerminalController) CloseTerminal(id string) error {
	s.closedIDs = append(s.closedIDs, id)
	return nil
}

func (s *stubTerminalController) SubscribeTerminal(id string) (TerminalSubscription, error) {
	if s.subErr != nil {
		return TerminalSubscription{}, s.subErr
	}
	return s.sub, nil
}

func TestTerminalRoutesRequireConfiguredController(t *testing.T) {
	handler := New(Options{
		Turn:  func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) { return Receipt{}, nil },
		Facts: Facts{Directory: "/work/default"},
	}).Handler()

	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/terminal/create", map[string]int{"cols": 80, "rows": 24}},
		{http.MethodGet, "/api/terminal/stream?id=term-1", nil},
		{http.MethodPost, "/api/terminal/write", map[string]string{"id": "term-1", "data": "ls\n"}},
		{http.MethodPost, "/api/terminal/resize", map[string]any{"id": "term-1", "cols": 100, "rows": 30}},
		{http.MethodPost, "/api/terminal/close", map[string]string{"id": "term-1"}},
	} {
		rec := doLocalReq(handler, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, want 501", tc.method, tc.path, rec.Code)
		}
	}
}

func TestTerminalCreateWriteResizeCloseAndDirectoryFallback(t *testing.T) {
	ctrl := &stubTerminalController{}
	host := &statusHost{directory: "/work/session-a"}
	handler := New(Options{
		Turn:     func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) { return Receipt{}, nil },
		Host:     host,
		Terminal: ctrl,
		Facts:    Facts{Directory: "/work/default"},
	}).Handler()

	rec := doLocalReq(handler, http.MethodPost, "/api/terminal/create", map[string]int{"cols": 120, "rows": 32})
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var info TerminalSessionInfo
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.ID != "term-1" || info.CWD != "/work/session-a" || ctrl.cols != 120 || ctrl.rows != 32 {
		t.Fatalf("unexpected create info=%+v cols=%d rows=%d", info, ctrl.cols, ctrl.rows)
	}

	// Empty session directory falls back to Facts.Directory.
	host.directory = ""
	rec2 := doLocalReq(handler, http.MethodPost, "/api/terminal/create", nil)
	if rec2.Code != http.StatusOK || ctrl.createdDir != "/work/default" || ctrl.cols != 80 || ctrl.rows != 24 {
		t.Fatalf("fallback create got code=%d dir=%q cols=%d rows=%d", rec2.Code, ctrl.createdDir, ctrl.cols, ctrl.rows)
	}

	if rec := doLocalReq(handler, http.MethodPost, "/api/terminal/write", map[string]string{"id": "term-1", "data": "pwd\r"}); rec.Code != http.StatusOK {
		t.Fatalf("write status = %d", rec.Code)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte{0x1b, '[', 'A'})
	if rec := doLocalReq(handler, http.MethodPost, "/api/terminal/write", map[string]string{"id": "term-1", "data": b64, "encoding": "base64"}); rec.Code != http.StatusOK {
		t.Fatalf("write b64 status = %d", rec.Code)
	}
	if len(ctrl.writes) != 2 || string(ctrl.writes[0]) != "pwd\r" || string(ctrl.writes[1]) != "\x1b[A" {
		t.Fatalf("writes = %q", ctrl.writes)
	}

	if rec := doLocalReq(handler, http.MethodPost, "/api/terminal/resize", map[string]any{"id": "term-1", "cols": 0, "rows": 20}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid resize = %d, want 400", rec.Code)
	}
	if rec := doLocalReq(handler, http.MethodPost, "/api/terminal/resize", map[string]any{"id": "term-1", "cols": 90, "rows": 25}); rec.Code != http.StatusOK {
		t.Fatalf("resize = %d", rec.Code)
	}
	if len(ctrl.resizes) != 1 || ctrl.resizes[0] != [2]int{90, 25} {
		t.Fatalf("resizes = %v", ctrl.resizes)
	}

	if rec := doLocalReq(handler, http.MethodPost, "/api/terminal/close", map[string]string{"id": "term-1"}); rec.Code != http.StatusOK {
		t.Fatalf("close = %d", rec.Code)
	}
	if len(ctrl.closedIDs) != 1 || ctrl.closedIDs[0] != "term-1" {
		t.Fatalf("closedIDs = %v", ctrl.closedIDs)
	}
}

func TestTerminalStreamReplaysBacklogAndLiveEvents(t *testing.T) {
	ch := make(chan TerminalEvent, 2)
	canceled := false
	ctrl := &stubTerminalController{
		sub: TerminalSubscription{
			Backlog: [][]byte{[]byte("zsh% ")},
			Events:  ch,
			Cancel:  func() { canceled = true },
		},
	}
	ch <- TerminalEvent{Type: "data", Data: []byte("ok\r\n")}
	ch <- TerminalEvent{Type: "exit", ExitCode: 0}
	close(ch)

	handler := New(Options{
		Turn:     func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) { return Receipt{}, nil },
		Terminal: ctrl,
		Facts:    Facts{Directory: "/work/default"},
	}).Handler()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/terminal/stream?id=term-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stream status = %d", rec.Code)
	}
	if !canceled {
		t.Fatal("expected subscription Cancel to be called")
	}

	var events []terminalWireEvent
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev terminalWireEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("unmarshal event %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want 3 events", events)
	}
	d0, _ := base64.StdEncoding.DecodeString(events[0].Data)
	d1, _ := base64.StdEncoding.DecodeString(events[1].Data)
	if string(d0) != "zsh% " || string(d1) != "ok\r\n" || events[2].Type != "exit" {
		t.Fatalf("unexpected decoded events: %q %q %+v", d0, d1, events[2])
	}

	ctrl.subErr = errors.New("not found")
	recMissing := doLocalReq(handler, http.MethodGet, "/api/terminal/stream?id=missing", nil)
	if recMissing.Code != http.StatusNotFound {
		t.Fatalf("missing stream = %d, want 404", recMissing.Code)
	}
}

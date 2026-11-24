package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type reloadableStatusHost struct {
	Host
	archived  bool
	directory string
	model     string
	reloadErr error
	reloads   int
}

func (h *reloadableStatusHost) SessionArchived() bool    { return h.archived }
func (h *reloadableStatusHost) SessionDirectory() string { return h.directory }
func (h *reloadableStatusHost) SessionModel() string     { return h.model }
func (h *reloadableStatusHost) ReloadConfig() error {
	h.reloads++
	if h.reloadErr != nil {
		return h.reloadErr
	}
	h.model = "p2/reloaded"
	return nil
}

func TestConfigReloadRouteAndLiveModelState(t *testing.T) {
	// Partial Host without ConfigReloader returns 501 cleanly.
	noReload := projectServer(t, &projectHost{}, nil)
	resp := postJSON(t, noReload.URL+"/api/config/reload", "")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("without ConfigReloader status = %d, want 501", resp.StatusCode)
	}

	host := &reloadableStatusHost{directory: "/work/app", model: "p1/initial"}
	server := projectServer(t, host, nil)

	// Reload failure returns 409 Conflict with error detail.
	host.reloadErr = errors.New("bad config")
	failResp := postJSON(t, server.URL+"/api/config/reload", "")
	if failResp.StatusCode != http.StatusConflict {
		t.Fatalf("failed reload status = %d, want 409", failResp.StatusCode)
	}

	// Reload success updates live model reported by /api/state and SSE hello.
	host.reloadErr = nil
	okResp := postJSON(t, server.URL+"/api/config/reload", "")
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("reload status = %d, want 200", okResp.StatusCode)
	}
	if host.reloads != 2 {
		t.Fatalf("reloads = %d, want 2", host.reloads)
	}

	stateResp, err := http.Get(server.URL + "/api/state")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	defer stateResp.Body.Close()
	var state struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(stateResp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Model != "p2/reloaded" {
		t.Fatalf("state.Model = %q, want p2/reloaded", state.Model)
	}

	sseResp, err := http.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	defer sseResp.Body.Close()
	scanner := bufio.NewScanner(sseResp.Body)
	var helloModel string
	sawHello := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "event: hello" {
			sawHello = true
			continue
		}
		if sawHello && strings.HasPrefix(line, "data: ") {
			var hello struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &hello); err != nil {
				t.Fatalf("decode hello: %v", err)
			}
			helloModel = hello.Model
			break
		}
	}
	if helloModel != "p2/reloaded" {
		t.Fatalf("hello.Model = %q, want p2/reloaded", helloModel)
	}
}

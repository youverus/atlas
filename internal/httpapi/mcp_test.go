package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type mcpStatusHost struct {
	Host
	state      MCPState
	refreshErr error
	refreshed  []string
}

func (h *mcpStatusHost) MCP() (MCPState, error) {
	return h.state, nil
}

func (h *mcpStatusHost) RefreshMCP(name string) (MCPState, error) {
	h.refreshed = append(h.refreshed, name)
	if h.refreshErr != nil {
		return MCPState{}, h.refreshErr
	}
	return h.state, nil
}

func (h *mcpStatusHost) SetMCPEnabled(_ context.Context, name string, enabled bool) (MCPState, error) {
	if name != h.state.Servers[0].Name || enabled {
		return MCPState{}, errors.New("unexpected change")
	}
	h.state.Servers[0].State = "disabled"
	h.state.Servers[0].Tools = nil
	return h.state, nil
}

func TestMCPDisableRouteReturnsAnEmptyToolList(t *testing.T) {
	host := &mcpStatusHost{state: MCPState{CanToggle: true, Servers: []MCPServerInfo{{Name: "docs", State: "connected"}}}}
	server := projectServer(t, host, nil)
	resp := postJSON(t, server.URL+"/api/mcp/enabled", `{"name":"docs","enabled":false}`)
	defer resp.Body.Close()
	var got MCPState
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK || !got.CanToggle || len(got.Servers) != 1 || got.Servers[0].State != "disabled" || got.Servers[0].Tools == nil || len(got.Servers[0].Tools) != 0 {
		t.Fatalf("disabled state = %+v, status = %d, error = %v", got, resp.StatusCode, err)
	}
}

func TestMCPRoutesReportAndRefresh(t *testing.T) {
	// Host without MCPStatus answers 501 cleanly.
	plain := projectServer(t, &projectHost{}, nil)
	resp, err := http.Get(plain.URL + "/api/mcp")
	if err != nil {
		t.Fatalf("get without MCPStatus: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("without MCPStatus status = %d, want 501", resp.StatusCode)
	}

	host := &mcpStatusHost{
		state: MCPState{
			ConfigPath: "/home/user/.atlas/mcp.json",
			Servers: []MCPServerInfo{
				{
					Name:      "docs",
					Version:   "1.0.0",
					Transport: "stdio",
					State:     "connected",
					Declared:  1,
					Tools: []MCPToolInfo{
						{Name: "search", WireName: "mcp__docs__search", Description: "Search docs"},
					},
				},
			},
		},
	}
	server := projectServer(t, host, nil)

	getResp, err := http.Get(server.URL + "/api/mcp")
	if err != nil {
		t.Fatalf("GET /api/mcp: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/mcp status = %d, want 200", getResp.StatusCode)
	}
	var got MCPState
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode MCPState: %v", err)
	}
	if got.ConfigPath != "/home/user/.atlas/mcp.json" || len(got.Servers) != 1 || got.Servers[0].Tools[0].WireName != "mcp__docs__search" {
		t.Fatalf("got = %+v", got)
	}

	refreshResp := postJSON(t, server.URL+"/api/mcp/refresh", `{"name":"docs"}`)
	defer refreshResp.Body.Close()
	if refreshResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mcp/refresh status = %d, want 200", refreshResp.StatusCode)
	}
	if len(host.refreshed) != 1 || host.refreshed[0] != "docs" {
		t.Fatalf("refreshed = %v, want [docs]", host.refreshed)
	}

	host.refreshErr = errors.New("not connected")
	errResp := postJSON(t, server.URL+"/api/mcp/refresh", "")
	errResp.Body.Close()
	if errResp.StatusCode != http.StatusConflict {
		t.Fatalf("failed refresh status = %d, want 409", errResp.StatusCode)
	}
}

package webhost

import (
	"context"
	"errors"
	"path/filepath"

	"pi-go/internal/config"
	"pi-go/internal/httpapi"
)

// MCP returns a snapshot of configured MCP servers and their tools without
// disturbing any active conversation turn.
func (h *Host) MCP() (httpapi.MCPState, error) {
	servers := h.kernel.MCPServers()
	if servers == nil {
		path := ""
		if home, err := config.DefaultHome(); err == nil {
			path = filepath.Join(home, config.MCPFileName)
		}
		return httpapi.MCPState{
			ConfigPath: path,
			Servers:    []httpapi.MCPServerInfo{},
		}, nil
	}
	snapshot := servers.Describe()
	out := make([]httpapi.MCPServerInfo, 0, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		tools := make([]httpapi.MCPToolInfo, 0, len(server.Tools))
		for _, remote := range server.Tools {
			tools = append(tools, httpapi.MCPToolInfo{
				Name:        remote.Name,
				Title:       remote.Title,
				WireName:    remote.WireName,
				Description: remote.Description,
			})
		}
		out = append(out, httpapi.MCPServerInfo{
			Name:         server.Name,
			Version:      server.Version,
			Transport:    server.Transport,
			State:        server.State,
			Declared:     server.Declared,
			Tools:        tools,
			Missing:      append([]string(nil), server.Missing...),
			Error:        server.Error,
			RefreshError: server.RefreshError,
		})
	}
	return httpapi.MCPState{
		ConfigPath: snapshot.ConfigPath,
		Servers:    out,
		CanToggle:  true,
	}, nil
}

// SetMCPEnabled saves and applies the user's switch for one MCP server.
func (h *Host) SetMCPEnabled(ctx context.Context, name string, enabled bool) (httpapi.MCPState, error) {
	servers := h.kernel.MCPServers()
	if servers == nil {
		return httpapi.MCPState{}, errors.New("mcp: disabled for this Atlas process")
	}
	if err := servers.SetEnabled(ctx, name, enabled); err != nil {
		return httpapi.MCPState{}, err
	}
	return h.MCP()
}

// RefreshMCP re-lists tools from one connected MCP server (or all connected
// servers when name is empty) and returns the updated snapshot.
func (h *Host) RefreshMCP(name string) (httpapi.MCPState, error) {
	if servers := h.kernel.MCPServers(); servers != nil {
		if err := servers.Refresh(name); err != nil {
			return httpapi.MCPState{}, err
		}
	}
	return h.MCP()
}

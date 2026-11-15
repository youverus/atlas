package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/mcp"
	"pi-go/internal/tool"
)

// namedTool is the smallest tool.Tool the footer and the grouping need: a name
// and nothing else. It exists so the tests of "which group does this tool belong
// to" do not have to start a server.
type namedTool struct{ name string }

func (t namedTool) Name() string                { return t.name }
func (t namedTool) Label() string               { return t.name }
func (t namedTool) Description() string         { return "" }
func (t namedTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t namedTool) Execute(context.Context, tool.Call, func(tool.Partial)) (tool.Result, error) {
	return tool.Result{}, nil
}

// TestMCPToolsShareOneFooterGroup is the footer rule: the grant a reader made was
// "this server", so its forty tools are one column and not forty.
func TestMCPToolsShareOneFooterGroup(t *testing.T) {
	set := workspaceTools(t.TempDir(), []tool.Tool{namedTool{"websearch"}}, []tool.Tool{
		namedTool{"mcp__fs__read_file"},
		namedTool{"mcp__fs__write_file"},
		namedTool{"mcp__docs__search"},
		namedTool{"not_mcp"},
	}, nil)

	if got := toolSummary(set); got != "fs+bash+web+mcp:fs+mcp:docs+not_mcp" {
		t.Fatalf("summary = %q, want the remote tools grouped by server", got)
	}
}

// TestMCPReportWithoutServers points at the file a reader would create. "There
// are none" on its own is a dead end.
func TestMCPReportWithoutServers(t *testing.T) {
	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := (mcpState{path: path}).report(context.Background(), nil, &out); err != nil {
		t.Fatalf("report: %v", err)
	}
	if !strings.Contains(out.String(), path) {
		t.Fatalf("output = %q, want the path %q", out.String(), path)
	}
}

// TestMCPReportListsServers covers the two states a table has to distinguish:
// connected with tools, and failed with a reason underneath it.
func TestMCPReportListsServers(t *testing.T) {
	servers := []*mcp.Server{
		{Name: "docs", Version: "2.0.0", Tools: []mcp.RemoteTool{
			{Server: "docs", Name: "search", WireName: "mcp__docs__search", Description: "look\nit\nup"},
		}},
		{Name: "fs", Err: errors.New(`exec: "npx": executable file not found in $PATH`)},
	}

	var out bytes.Buffer
	if err := writeMCPServers(&out, servers); err != nil {
		t.Fatalf("report: %v", err)
	}
	text := out.String()
	for _, want := range []string{"SERVER", "docs", "2.0.0", "connected", "fs", "failed", "executable file not found"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output = %q, want it to contain %q", text, want)
		}
	}
}

// TestMCPToolsListOneServer shows both names: the server's spelling, which the
// protocol uses, and the model's, which is the one worth copying into a bug
// report.
func TestMCPToolsListOneServer(t *testing.T) {
	server := &mcp.Server{Name: "docs", Version: "2.0.0", Tools: []mcp.RemoteTool{
		{Server: "docs", Name: "search", WireName: "mcp__docs__search", Description: "look it up"},
	}}

	var out bytes.Buffer
	if err := writeMCPTools(&out, server); err != nil {
		t.Fatalf("writeMCPTools: %v", err)
	}
	for _, want := range []string{"search", "mcp__docs__search", "look it up"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want it to contain %q", out.String(), want)
		}
	}
}

// TestMCPReportShowsNarrowedCounts is the third part of A11: /mcp reads the same
// live set the model does, so a whitelist and a tool that arrived later are
// visible where the reader looks. A count on its own cannot say whether the server
// has two tools or the session was allowed two of five.
func TestMCPReportShowsNarrowedCounts(t *testing.T) {
	servers := []*mcp.Server{
		{Name: "wide", Version: "1.0.0", Declared: 5, Tools: []mcp.RemoteTool{
			{Server: "wide", Name: "read_file", WireName: "mcp__wide__read_file"},
			{Server: "wide", Name: "list_dir", WireName: "mcp__wide__list_dir"},
		}},
		{Name: "plain", Version: "1.0.0", Declared: 2, Tools: []mcp.RemoteTool{
			{Server: "plain", Name: "a", WireName: "mcp__plain__a"},
			{Server: "plain", Name: "b", WireName: "mcp__plain__b"},
		}},
	}

	var out bytes.Buffer
	if err := writeMCPServers(&out, servers); err != nil {
		t.Fatalf("writeMCPServers: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "2 of 5") {
		t.Fatalf("output = %q, want the narrowed server to show both numbers", text)
	}
	if strings.Contains(text, "2 of 2") {
		t.Fatalf("output = %q, want an unnarrowed server to show one number", text)
	}
}

// TestMCPToolsReportMissingAndStale covers the two lines under the tool table: a
// whitelist naming a tool the server does not declare, and a tool list that could
// not be read again. Both are the reader's to act on, and neither is a failed
// server.
func TestMCPToolsReportMissingAndStale(t *testing.T) {
	server := &mcp.Server{
		Name:       "wide",
		Version:    "1.0.0",
		Declared:   5,
		Missing:    []string{"read_flie"},
		RefreshErr: errors.New("tools/list: the server hung up"),
		Tools: []mcp.RemoteTool{
			{Server: "wide", Name: "read_file", WireName: "mcp__wide__read_file", Description: "read"},
		},
	}

	var out bytes.Buffer
	if err := writeMCPTools(&out, server); err != nil {
		t.Fatalf("writeMCPTools: %v", err)
	}
	text := out.String()
	for _, want := range []string{"read_flie", "does not declare", "could not be read again", "hung up"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output = %q, want it to contain %q", text, want)
		}
	}
}

// TestMCPReportSurvivesAStaleList is the table's version of the same fact: the
// failure goes underneath, where an error has room, and the count is the list the
// server gave last.
func TestMCPReportSurvivesAStaleList(t *testing.T) {
	servers := []*mcp.Server{{
		Name:       "wide",
		Version:    "1.0.0",
		Declared:   1,
		RefreshErr: errors.New("tools/list is not available right now"),
		Tools: []mcp.RemoteTool{
			{Server: "wide", Name: "read_file", WireName: "mcp__wide__read_file"},
		},
	}}

	var out bytes.Buffer
	if err := writeMCPServers(&out, servers); err != nil {
		t.Fatalf("writeMCPServers: %v", err)
	}
	text := out.String()
	if strings.Contains(text, "failed") {
		t.Fatalf("output = %q, want the server to stay connected: only its list is stale", text)
	}
	if !strings.Contains(text, "not available right now") {
		t.Fatalf("output = %q, want the reason underneath the table", text)
	}
}

// TestMCPReportUnknownServer is the typo case: an error that names what was
// asked for beats an empty table.
func TestMCPReportUnknownServer(t *testing.T) {
	var out bytes.Buffer
	err := (mcpState{}).report(context.Background(), []string{"nope"}, &out)
	if err == nil {
		t.Fatal("report succeeded, want an error for an unknown server")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want it to name the server", err)
	}
}

// TestMCPReportNeedsLogin is the row a login produces: the state word in the
// column, and the command that changes it underneath. The two are separate
// because the column has room for two words and the command is longer.
func TestMCPReportNeedsLogin(t *testing.T) {
	servers := []*mcp.Server{{Name: "linear", NeedsLogin: true}}

	var out bytes.Buffer
	if err := writeMCPServers(&out, servers); err != nil {
		t.Fatalf("writeMCPServers: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "needs login") {
		t.Fatalf("output = %q, want the state column to say so", text)
	}
	if !strings.Contains(text, `/mcp login linear`) {
		t.Fatalf("output = %q, want the command that changes the state", text)
	}
	if strings.Contains(text, "failed") {
		t.Fatalf("output = %q, want no failure: needing a login is not one", text)
	}

	var tools bytes.Buffer
	if err := writeMCPTools(&tools, servers[0]); err != nil {
		t.Fatalf("writeMCPTools: %v", err)
	}
	if !strings.Contains(tools.String(), `/mcp login linear`) {
		t.Fatalf("output = %q, want the same way forward", tools.String())
	}
}

// TestMCPLoginRefuses is the argument half of /mcp login: the three mistakes a
// reader can make before any network call, each answered with what to type
// instead.
func TestMCPLoginRefuses(t *testing.T) {
	state := mcpState{
		path:  filepath.Join(t.TempDir(), "mcp.json"),
		specs: []mcp.Spec{{Name: "headers-only", URL: "https://example.com/mcp"}},
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no name", []string{"login"}, "takes one server name"},
		{"two names", []string{"login", "a", "b"}, "takes one server name"},
		{"unknown", []string{"login", "nope"}, "no server named"},
		{"no auth", []string{"login", "headers-only"}, `no "auth": "oauth"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := state.report(context.Background(), tc.args, &out)
			if err == nil {
				t.Fatal("report succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if out.Len() != 0 {
				t.Fatalf("output = %q, want nothing: no login was started", out.String())
			}
		})
	}
}

// TestMCPForReadsTheConfiguredHome pins the wiring: mcpFor resolves the home
// directory, reads the file there, and reports a broken one as an error rather
// than as an empty session.
func TestMCPForReadsTheConfiguredHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)

	state, notes, err := mcpFor(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("mcpFor: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
	if state.path != filepath.Join(home, config.MCPFileName) {
		t.Fatalf("path = %q, want the file under ATLAS_HOME", state.path)
	}
	if len(state.tools()) != 0 {
		t.Fatalf("tools = %d, want none without a file", len(state.tools()))
	}

	if err := os.WriteFile(state.path, []byte(`{"mcpServers": {"x": {"type": "sse"}}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := mcpFor(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("mcpFor succeeded, want the config refusal")
	} else if !strings.Contains(err.Error(), "streamable HTTP") {
		t.Fatalf("error = %v, want the sse hint", err)
	}
}

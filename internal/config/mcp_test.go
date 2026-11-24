package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpHome writes <home>/mcp.json and returns the home directory.
func mcpHome(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	write(t, home, MCPFileName, content)
	return home
}

// mcpProject writes <cwd>/.atlas/mcp.json and returns the working directory.
func mcpProject(t *testing.T, content string) string {
	t.Helper()
	cwd := t.TempDir()
	dir := filepath.Join(cwd, mcpProjectDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	write(t, dir, MCPFileName, content)
	return cwd
}

// TestLoadMCPResolvesTheTwoFiles covers the shape a user writes: a stdio server
// and an HTTP one, one of them disabled, an environment reference, and the
// project file switching a third server off.
func TestLoadMCPResolvesTheTwoFiles(t *testing.T) {
	t.Setenv("ATLAS_TEST_TOKEN", "secret")
	home := mcpHome(t, `{
	  "mcpServers": {
	    "fs": {
	      "command": "npx",
	      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/work"],
	      "env": {"TOKEN": "${ATLAS_TEST_TOKEN}"},
	      "cwd": "/work"
	    },
	    "docs": {
	      "type": "http",
	      "url": "https://example.com/mcp",
	      "headers": {"Authorization": "Bearer ${ATLAS_TEST_TOKEN}"},
	      "timeout": 30
	    },
	    "off": {"command": "true", "enabled": false}
	  }
	}`)
	cwd := mcpProject(t, `{"mcpServers": {"docs": {"enabled": false}}}`)

	cfg, err := LoadMCP(home, cwd)
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %v, want only fs (docs is switched off by the project, off is disabled)", names(cfg.Servers))
	}
	server := cfg.Servers[0]
	if server.Name != "fs" {
		t.Fatalf("server = %q, want fs", server.Name)
	}
	if got := server.Env["TOKEN"]; got != "secret" {
		t.Fatalf("env TOKEN = %q, want the expanded value", got)
	}
	if server.Cwd != "/work" {
		t.Fatalf("cwd = %q, want /work", server.Cwd)
	}
	if cfg.Project == "" {
		t.Fatal("project file was not recorded")
	}
	if len(cfg.Missing) != 0 {
		t.Fatalf("missing = %v, want none", cfg.Missing)
	}
}

func TestLoadMCPUserSwitchesOverrideProjectAndDeclarations(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers":{"lint":{"command":"lint-server","enabled":false}}}`)
	write(t, home, MCPFileName, `{"mcpServers":{
	  "docs":{"url":"https://example.com/mcp","headers":{"Authorization":"${ATLAS_MCP_DISABLED_TOKEN}"}},
	  "off":{"command":"true","enabled":false}
	}}`)
	cwd := mcpProject(t, `{"mcpServers":{"docs":{"enabled":true},"lint":{"enabled":false}}}`)
	t.Setenv("ATLAS_MCP_DISABLED_TOKEN", "")
	if err := os.Unsetenv("ATLAS_MCP_DISABLED_TOKEN"); err != nil {
		t.Fatal(err)
	}
	for name, enabled := range map[string]bool{"docs": false, "lint": true, "removed": true} {
		if err := SaveMCPEnabled(home, name, enabled); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := LoadMCP(home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(cfg.Servers), ","); got != "lint" {
		t.Fatalf("enabled = %s, want lint", got)
	}
	if got := strings.Join(names(cfg.Disabled), ","); got != "docs,off" {
		t.Fatalf("disabled = %s, want docs,off", got)
	}
	if len(cfg.Missing) != 0 {
		t.Fatalf("missing = %v, want no startup warnings for disabled servers", cfg.Missing)
	}
	if got := cfg.Disabled[0].URL; got != "https://example.com/mcp" {
		t.Fatalf("disabled server URL = %q, want original URL available for enabling", got)
	}
}

// TestLoadMCPInfersTheType pins the rule that makes the entries portable: the
// transport is what the fields say, and "type" is only a way to be explicit.
func TestLoadMCPInfersTheType(t *testing.T) {
	home := mcpHome(t, `{"mcpServers": {
	  "a": {"command": "true"},
	  "b": {"url": "http://127.0.0.1:9/mcp"},
	  "c": {"type": "streamable-http", "url": "http://127.0.0.1:9/mcp"}
	}}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 3 {
		t.Fatalf("servers = %v, want all three", names(cfg.Servers))
	}
}

// TestLoadMCPRecordsMissingVariables is the warning's data: an unset variable
// stays literal in the message the client sees, and its name is reported once.
func TestLoadMCPRecordsMissingVariables(t *testing.T) {
	home := mcpHome(t, `{"mcpServers": {"fs": {"command": "x", "env": {
	  "A": "${ATLAS_UNSET_ONE}", "B": "${ATLAS_UNSET_ONE}", "C": "${ATLAS_UNSET_TWO}"
	}}}}`)
	t.Setenv("ATLAS_UNSET_ONE", "")
	os.Unsetenv("ATLAS_UNSET_ONE")
	os.Unsetenv("ATLAS_UNSET_TWO")

	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	want := []string{"ATLAS_UNSET_ONE", "ATLAS_UNSET_TWO"}
	if strings.Join(cfg.Missing, ",") != strings.Join(want, ",") {
		t.Fatalf("missing = %v, want %v", cfg.Missing, want)
	}
	if got := cfg.Servers[0].Env["A"]; got != "${ATLAS_UNSET_ONE}" {
		t.Fatalf("env A = %q, want the literal left in place", got)
	}
}

// TestLoadMCPMissingFileIsNotAnError is the state every install starts in. The
// path still comes back, because "there are none" is not actionable without
// saying where they would go.
func TestLoadMCPMissingFileIsNotAnError(t *testing.T) {
	home := t.TempDir()
	cfg, err := LoadMCP(home, t.TempDir())
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %v, want none", cfg.Servers)
	}
	if cfg.Global != filepath.Join(home, MCPFileName) {
		t.Fatalf("global = %q, want the path a user would create", cfg.Global)
	}
}

// TestLoadMCPRefuses walks the mistakes a file can contain. Each case is a
// refusal with a reason; none of them is a warning, because a config that
// half-loaded leaves the reader without a picture of what is running.
func TestLoadMCPRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		home    string
		project string
		want    string
	}{
		{
			name: "both transports",
			home: `{"mcpServers": {"x": {"command": "true", "url": "http://127.0.0.1/mcp"}}}`,
			want: "not both",
		},
		{
			name: "no transport",
			home: `{"mcpServers": {"x": {"args": ["-y"]}}}`,
			want: "needs a \"command\"",
		},
		{
			name: "sse",
			home: `{"mcpServers": {"x": {"type": "sse", "url": "http://127.0.0.1/sse"}}}`,
			want: "streamable HTTP",
		},
		{
			name: "unknown type",
			home: `{"mcpServers": {"x": {"type": "grpc", "command": "true"}}}`,
			want: "not one of",
		},
		{
			name: "headers on a command server",
			home: `{"mcpServers": {"x": {"command": "true", "headers": {"A": "B"}}}}`,
			want: "belong to a \"url\" server",
		},
		{
			name: "args on a url server",
			home: `{"mcpServers": {"x": {"url": "http://127.0.0.1/mcp", "args": ["-y"]}}}`,
			want: "belong to a \"command\" server",
		},
		{
			name: "bad url",
			home: `{"mcpServers": {"x": {"url": "ftp://example.com/mcp"}}}`,
			want: "http://",
		},
		{
			name: "header with a newline",
			home: `{"mcpServers": {"x": {"url": "http://127.0.0.1/mcp", "headers": {"A": "b\nc"}}}}`,
			want: "line break",
		},
		{
			name: "zero timeout",
			home: `{"mcpServers": {"x": {"command": "true", "timeout": 0}}}`,
			want: "positive",
		},
		{
			name: "relative cwd",
			home: `{"mcpServers": {"x": {"command": "true", "cwd": "src"}}}`,
			want: "absolute",
		},
		{
			name: "bad name",
			home: `{"mcpServers": {"my server": {"command": "true"}}}`,
			want: "letters, digits",
		},
		{
			name: "names differing by a dash",
			home: `{"mcpServers": {"a-b": {"command": "true"}, "a_b": {"command": "true"}}}`,
			want: "differ only",
		},
		{
			name: "unknown field",
			home: `{"mcpServers": {"x": {"command": "true", "exposure": "direct"}}}`,
			want: "unknown field",
		},
		{
			name: "unknown top level field",
			home: `{"servers": {}}`,
			want: "unknown field",
		},
		{
			name:    "project sets a command",
			home:    `{"mcpServers": {"x": {"command": "true"}}}`,
			project: `{"mcpServers": {"x": {"enabled": false, "command": "rm"}}}`,
			want:    "only set \"enabled\"",
		},
		{
			name:    "project names an unknown server",
			home:    `{"mcpServers": {"x": {"command": "true"}}}`,
			project: `{"mcpServers": {"y": {"enabled": false}}}`,
			want:    "no server of that name",
		},
		{
			name: "an empty tool name",
			home: `{"mcpServers": {"x": {"command": "true", "tools": [""]}}}`,
			want: "empty string",
		},
		{
			name: "a tool listed twice",
			home: `{"mcpServers": {"x": {"command": "true", "tools": ["a", "a"]}}}`,
			want: "listed twice",
		},
		{
			name:    "project narrows the tools",
			home:    `{"mcpServers": {"x": {"command": "true"}}}`,
			project: `{"mcpServers": {"x": {"tools": ["read_file"]}}}`,
			want:    "only set \"enabled\"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := mcpHome(t, tc.home)
			cwd := ""
			if tc.project != "" {
				cwd = mcpProject(t, tc.project)
			}
			_, err := LoadMCP(home, cwd)
			if err == nil {
				t.Fatal("LoadMCP succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestConfigRefusesOAuth is acceptance 9: the four ways a file can ask for an
// authorization that cannot succeed, plus the fifth (scopes on their own) that
// exists because a key silently ignored is the failure this file prevents.
func TestConfigRefusesOAuth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		home    string
		project string
		want    string
	}{
		{
			name: "an auth atlas does not have",
			home: `{"mcpServers": {"x": {"url": "https://example.com/mcp", "auth": "sigv4"}}}`,
			want: "is not a mode atlas has",
		},
		{
			name: "oauth on a command server",
			home: `{"mcpServers": {"x": {"command": "true", "auth": "oauth"}}}`,
			want: "no HTTP endpoint to authorize against",
		},
		{
			name: "oauth and an authorization header",
			home: `{"mcpServers": {"x": {"url": "https://example.com/mcp", "auth": "oauth",
			  "headers": {"authorization": "Bearer sk-test"}}}}`,
			want: "two credentials",
		},
		{
			name:    "a project file asking for oauth",
			home:    `{"mcpServers": {"x": {"url": "https://example.com/mcp"}}}`,
			project: `{"mcpServers": {"x": {"enabled": true, "auth": "oauth"}}}`,
			want:    "only set \"enabled\"",
		},
		{
			name:    "a project file narrowing the scopes",
			home:    `{"mcpServers": {"x": {"url": "https://example.com/mcp", "auth": "oauth"}}}`,
			project: `{"mcpServers": {"x": {"enabled": true, "scopes": ["read"]}}}`,
			want:    "only set \"enabled\"",
		},
		{
			name: "scopes without auth",
			home: `{"mcpServers": {"x": {"url": "https://example.com/mcp", "scopes": ["read"]}}}`,
			want: `"scopes" only mean something with`,
		},
		{
			name: "a scope with a space in it",
			home: `{"mcpServers": {"x": {"url": "https://example.com/mcp", "auth": "oauth",
			  "scopes": ["read write"]}}}`,
			want: "separates scopes",
		},
		{
			name: "an empty scope",
			home: `{"mcpServers": {"x": {"url": "https://example.com/mcp", "auth": "oauth",
			  "scopes": [""]}}}`,
			want: "is empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := mcpHome(t, tc.home)
			cwd := ""
			if tc.project != "" {
				cwd = mcpProject(t, tc.project)
			}
			_, err := LoadMCP(home, cwd)
			if err == nil {
				t.Fatal("LoadMCP succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestLoadMCPResolvesOAuth is the other half: what a correct authorization entry
// becomes once it is loaded. Scopes are copied rather than shared, because the
// caller hands them to a client that keeps them for the session.
func TestLoadMCPResolvesOAuth(t *testing.T) {
	home := mcpHome(t, `{"mcpServers": {
	  "linear": {"url": "https://mcp.linear.app/mcp", "auth": "oauth", "scopes": ["read", "write"]},
	  "plain": {"url": "https://example.com/mcp"}
	}}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("servers = %v, want both", names(cfg.Servers))
	}
	linear := cfg.Servers[0]
	if linear.Name != "linear" {
		t.Fatalf("server = %q, want linear", linear.Name)
	}
	if linear.Auth != "oauth" {
		t.Fatalf("auth = %q, want oauth", linear.Auth)
	}
	if strings.Join(linear.Scopes, " ") != "read write" {
		t.Fatalf("scopes = %v, want read and write in order", linear.Scopes)
	}
	if plain := cfg.Servers[1]; plain.Auth != "" || len(plain.Scopes) != 0 {
		t.Fatalf("plain = %+v, want no authorization at all", plain)
	}
}

// TestLoadMCPKeepsTheToolWhitelist is the configuration half of A11: the names
// travel to the client exactly as written, because they are the server's spelling
// and a rewrite here would be a name the server does not answer to.
func TestLoadMCPKeepsTheToolWhitelist(t *testing.T) {
	home := mcpHome(t, `{"mcpServers": {"x": {"command": "true", "tools": ["read_file", "list-dir"]}}}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %v, want one", names(cfg.Servers))
	}
	got := cfg.Servers[0].Tools
	if len(got) != 2 || got[0] != "read_file" || got[1] != "list-dir" {
		t.Fatalf("tools = %v, want both names unchanged", got)
	}
}

// names lists the servers in order, for messages that would otherwise print
// structs.
func names(servers []MCPServer) []string {
	out := make([]string, len(servers))
	for index, server := range servers {
		out[index] = server.Name
	}
	return out
}

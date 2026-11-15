package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// pluginHome writes <home>/plugins/<name>/plugin.json and returns the home
// directory, which is the only place plugins are read from.
func pluginHome(t *testing.T, name, content string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, PluginDirName, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	write(t, dir, pluginFileName, content)
	return home
}

// TestLoadPluginsReadsTheTwoContributions is the shape a plugin has: one line
// for a human, a directory of skills, and servers in the format mcp.json already
// uses.
func TestLoadPluginsReadsTheTwoContributions(t *testing.T) {
	home := pluginHome(t, "review", `{
	  "description": "Review helpers.",
	  "skills": "skills",
	  "mcpServers": {
	    "lint": {"command": "lint-server", "args": ["--stdio"]},
	    "docs": {"url": "https://example.com/mcp"}
	  }
	}`)
	skills := filepath.Join(home, PluginDirName, "review", "skills")
	if err := os.MkdirAll(filepath.Join(skills, "release"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := LoadPlugins(home)
	if len(cfg.Problems) != 0 {
		t.Fatalf("problems = %v, want none", cfg.Problems)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("plugins = %v, want one", cfg.Plugins)
	}
	found := cfg.Plugins[0]
	if found.Name != "review" || found.Description != "Review helpers." {
		t.Fatalf("plugin = %+v, want the name and the description", found)
	}
	if found.Dir != filepath.Join(home, PluginDirName, "review") {
		t.Fatalf("dir = %q", found.Dir)
	}
	if found.SkillsDir != skills {
		t.Fatalf("skills = %q, want %q", found.SkillsDir, skills)
	}
	if got := found.ServerNames(); !reflect.DeepEqual(got, []string{"docs", "lint"}) {
		t.Fatalf("servers = %v, want them sorted", got)
	}
}

// A machine that has never installed one has no plugins directory at all, and
// that is the normal state rather than a finding. The path is still reported: it
// is the answer to "where would I put one".
func TestLoadPluginsIsQuietWithoutADirectory(t *testing.T) {
	home := t.TempDir()
	cfg := LoadPlugins(home)
	if len(cfg.Plugins) != 0 || len(cfg.Problems) != 0 {
		t.Fatalf("plugins = %v, problems = %v, want neither", cfg.Plugins, cfg.Problems)
	}
	if cfg.Dir != filepath.Join(home, PluginDirName) {
		t.Fatalf("dir = %q, want the plugins path even when it does not exist", cfg.Dir)
	}
}

// A directory with no plugin.json is not a plugin, and saying so on every start
// would make an unrelated file in that directory a warning.
func TestLoadPluginsIgnoresADirectoryThatIsNotAPlugin(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, PluginDirName, "notes"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := LoadPlugins(home)
	if len(cfg.Plugins) != 0 || len(cfg.Problems) != 0 {
		t.Fatalf("plugins = %v, problems = %v, want neither", cfg.Plugins, cfg.Problems)
	}
}

// TestLoadPluginsRefuses walks what a plugin.json can get wrong. None of these
// is fatal for the session: each is one directory that does not load, and the
// line names the file.
func TestLoadPluginsRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string // the plugin directory name, "review" unless given
		file string
		want string
	}{
		{
			name: "an unknown key",
			file: `{"skills": "skills", "version": "1.2.0"}`,
			want: "unknown field",
		},
		{
			name: "nothing declared",
			file: `{"description": "a note about nothing"}`,
			want: "contributes nothing",
		},
		{
			name: "an absolute skills path",
			file: `{"skills": "/etc"}`,
			want: "absolute path",
		},
		{
			name: "a skills path that leaves",
			file: `{"skills": "../review/skills"}`,
			want: "leaves the plugin directory",
		},
		{
			name: "the plugin directory itself",
			file: `{"skills": "."}`,
			want: "leaves the plugin directory",
		},
		{
			name: "two transports on one server",
			file: `{"mcpServers": {"x": {"command": "true", "url": "https://example.com/mcp"}}}`,
			want: "not both",
		},
		{
			name: "server names differing by a dash",
			file: `{"mcpServers": {"a-b": {"command": "true"}, "a_b": {"command": "true"}}}`,
			want: "differ only",
		},
		{
			name: "two json values",
			file: `{"skills": "skills"} {"skills": "other"}`,
			want: "trailing data",
		},
		{
			name: "not an object",
			file: `["skills"]`,
			want: "cannot unmarshal",
		},
		{
			name: "a directory name that cannot be typed",
			dir:  "my plugin",
			file: `{"skills": "skills"}`,
			want: "letters, digits",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir
			if dir == "" {
				dir = "review"
			}
			home := pluginHome(t, dir, tc.file)
			cfg := LoadPlugins(home)
			if len(cfg.Plugins) != 0 {
				t.Fatalf("plugins = %v, want none", cfg.Plugins)
			}
			if len(cfg.Problems) != 1 || !strings.Contains(cfg.Problems[0], tc.want) {
				t.Fatalf("problems = %v, want one containing %q", cfg.Problems, tc.want)
			}
			if !strings.HasPrefix(cfg.Problems[0], "plugin: ") {
				t.Fatalf("problem = %q, want the plugin: prefix", cfg.Problems[0])
			}
		})
	}
}

// A declared skills directory that is not there costs the skills and nothing
// else: the servers half may still be worth starting.
func TestLoadPluginsKeepsTheServersOfAMissingSkillsDirectory(t *testing.T) {
	home := pluginHome(t, "review", `{"skills": "gone", "mcpServers": {"lint": {"command": "true"}}}`)
	cfg := LoadPlugins(home)
	if len(cfg.Plugins) != 1 {
		t.Fatalf("plugins = %v, want the plugin to load for its servers", cfg.Plugins)
	}
	if cfg.Plugins[0].SkillsDir != "" {
		t.Fatalf("skills = %q, want none", cfg.Plugins[0].SkillsDir)
	}
	if len(cfg.Problems) != 1 || !strings.Contains(cfg.Problems[0], "is not a directory") {
		t.Fatalf("problems = %v, want one about the directory", cfg.Problems)
	}
}

// One broken plugin must not cost the others: the scan continues and both the
// plugin and the line about it are reported.
func TestLoadPluginsKeepsTheGoodOnes(t *testing.T) {
	home := pluginHome(t, "broken", `{"skills": "/etc"}`)
	good := filepath.Join(home, PluginDirName, "good")
	if err := os.MkdirAll(good, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, good, pluginFileName, `{"mcpServers": {"lint": {"command": "true"}}}`)

	cfg := LoadPlugins(home)
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].Name != "good" {
		t.Fatalf("plugins = %+v, want only good", cfg.Plugins)
	}
	if len(cfg.Problems) != 1 {
		t.Fatalf("problems = %v, want one", cfg.Problems)
	}
}

// TestLoadMCPFoldsPluginServers is the join: a server a plugin declared reaches
// the same list a server from mcp.json reaches, with ${NAME} expanded once and
// the missing names collected in one place.
func TestLoadMCPFoldsPluginServers(t *testing.T) {
	t.Setenv("ATLAS_PLUGIN_TOKEN", "secret")
	home := pluginHome(t, "review", `{"mcpServers": {
	  "lint": {"command": "lint-server", "env": {"TOKEN": "${ATLAS_PLUGIN_TOKEN}"}}
	}}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if got := names(cfg.Servers); !reflect.DeepEqual(got, []string{"lint"}) {
		t.Fatalf("servers = %v, want the plugin's one", got)
	}
	if got := cfg.Servers[0].Env["TOKEN"]; got != "secret" {
		t.Fatalf("env TOKEN = %q, want the expanded value", got)
	}
	if len(cfg.Missing) != 0 || len(cfg.Notes) != 0 {
		t.Fatalf("missing = %v, notes = %v, want neither", cfg.Missing, cfg.Notes)
	}
}

// A plugin's entry goes through the same validation mcp.json's does, and a
// failure is the plugin's own line: the rest of the machine still loads.
func TestAPluginServerIsValidated(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers": {"lint": {"command": "true", "url": "https://example.com/mcp"}}}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %v, want the invalid one dropped", names(cfg.Servers))
	}
	if len(cfg.Notes) != 1 || !strings.Contains(cfg.Notes[0], "not both") {
		t.Fatalf("notes = %v, want the validation message", cfg.Notes)
	}
}

// A plugin server is a server like any other, so the project file can switch it
// off — that is the one thing a checkout is allowed to say about servers.
func TestAProjectFileCanSwitchAPluginServerOff(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers": {"lint": {"command": "true"}}}`)
	cwd := mcpProject(t, `{"mcpServers": {"lint": {"enabled": false}}}`)
	cfg, err := LoadMCP(home, cwd)
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %v, want the project to have switched the plugin's off", names(cfg.Servers))
	}
}

// The user's own file wins a name, and the losing server is a note rather than a
// refusal: the plugin is not the file the reader is editing.
func TestTheHomeFileWinsAServerName(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers": {"fs": {"command": "the-plugin"}}}`)
	write(t, home, MCPFileName, `{"mcpServers": {"fs": {"command": "the-user"}}}`)

	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Command != "the-user" {
		t.Fatalf("servers = %+v, want the user's file to win", cfg.Servers)
	}
	if len(cfg.Notes) != 1 || !strings.Contains(cfg.Notes[0], "already declares that name") {
		t.Fatalf("notes = %v, want one about the name", cfg.Notes)
	}
}

// The same rule covers names that differ only in "-" and "_": both would produce
// one mcp__ prefix, so the second is dropped with the pair spelled out.
func TestANearMissOnAServerNameIsNoted(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers": {"my_server": {"command": "the-plugin"}}}`)
	write(t, home, MCPFileName, `{"mcpServers": {"my-server": {"command": "the-user"}}}`)

	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if got := names(cfg.Servers); !reflect.DeepEqual(got, []string{"my-server"}) {
		t.Fatalf("servers = %v, want only the user's", got)
	}
	if len(cfg.Notes) != 1 || !strings.Contains(cfg.Notes[0], "differs only") {
		t.Fatalf("notes = %v, want one about the dash", cfg.Notes)
	}
}

// The other order: mcp.json may not exist at all, and a plugin is then the only
// thing declaring a server. This is the case the old early return would have
// dropped on the floor.
func TestPluginsAreTheOnlySource(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers": {"lint": {"command": "true"}}}`)
	cfg, err := LoadMCP(home, t.TempDir())
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if got := names(cfg.Servers); !reflect.DeepEqual(got, []string{"lint"}) {
		t.Fatalf("servers = %v, want the plugin's", got)
	}
	if cfg.Global != filepath.Join(home, MCPFileName) {
		t.Fatalf("global = %q", cfg.Global)
	}
}

// A plugin that declares a server switched off contributes nothing to the
// session, which is the same meaning "enabled": false has in mcp.json.
func TestAPluginServerCanBeDeclaredOff(t *testing.T) {
	home := pluginHome(t, "review", `{"mcpServers": {"lint": {"command": "true", "enabled": false}}}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %v, want none", names(cfg.Servers))
	}
}

// A broken plugin is a note on this path, not an error: the session still
// starts, and the reader is told which directory to fix.
func TestABrokenPluginIsANoteOnTheMCPPath(t *testing.T) {
	home := pluginHome(t, "review", `{"skills": "/etc"}`)
	cfg, err := LoadMCP(home, "")
	if err != nil {
		t.Fatalf("LoadMCP: %v", err)
	}
	if len(cfg.Notes) != 1 || !strings.Contains(cfg.Notes[0], "plugin: ") {
		t.Fatalf("notes = %v, want the plugin's problem", cfg.Notes)
	}
}

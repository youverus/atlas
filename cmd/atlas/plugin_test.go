package main

import (
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/skill"
)

// pluginAt writes <home>/plugins/<name>/plugin.json.
func pluginAt(t *testing.T, home, name, content string) string {
	t.Helper()
	dir := filepath.Join(home, config.PluginDirName, name)
	putFile(t, filepath.Join(dir, "plugin.json"), content)
	return dir
}

// /plugin answers the two questions a reader has: what is installed, and what
// does each one add to this session. The counts come from the same scan the
// prompt and /skill use, so the number is what the session got rather than what
// the directory holds.
func TestPluginListsWhatIsInstalled(t *testing.T) {
	home := emptyHome(t)
	dir := pluginAt(t, home, "review", `{
	  "description": "Review helpers.",
	  "skills": "skills",
	  "mcpServers": {"lint": {"command": "true"}, "docs": {"command": "true"}}
	}`)
	putFile(t, filepath.Join(dir, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))

	cwd := t.TempDir()
	printed, err := runCommand(t, commandApp(t, memoryAt(t, cwd), cwd), "/plugin")
	if err != nil {
		t.Fatalf("/plugin: %v", err)
	}
	for _, want := range []string{
		"NAME", "SKILLS", "SERVERS", "DESCRIPTION", "LOCATION",
		"review",
		"docs, lint",
		"Review helpers.",
		dir,
		"/plugin lists what is installed",
	} {
		if !strings.Contains(printed, want) {
			t.Fatalf("output = %q, want it to contain %q", printed, want)
		}
	}
}

// The count is what this session got out of the plugin, not what its directory
// holds: a skill the user replaced with their own is not counted twice.
func TestTheSkillCountIsPerPluginDirectory(t *testing.T) {
	plugin := config.Plugin{Dir: "/home/.atlas/plugins/review"}
	skills := []skill.Skill{
		{Name: "release", Path: "/home/.atlas/plugins/review/skills/release/SKILL.md"},
		{Name: "deploy", Path: "/home/.atlas/plugins/review/other/SKILL.md"},
		{Name: "mine", Path: "/home/.atlas/skills/mine/SKILL.md"},
		// A sibling whose name starts with the same characters is not a child.
		{Name: "elsewhere", Path: "/home/.atlas/plugins/review-two/SKILL.md"},
	}
	if got := countPluginSkills(plugin, skills); got != 2 {
		t.Fatalf("count = %d, want the two under the plugin", got)
	}
}

// A machine with none is the normal state, and the answer to "where would I put
// one" is the path.
func TestPluginSaysWhereToPutOne(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	printed, err := runCommand(t, commandApp(t, memoryAt(t, cwd), cwd), "/plugin")
	if err != nil {
		t.Fatalf("/plugin: %v", err)
	}
	if !strings.Contains(printed, "no plugins are installed") {
		t.Fatalf("output = %q, want the empty-state sentence", printed)
	}
	if !strings.Contains(printed, filepath.Join(home, config.PluginDirName)) {
		t.Fatalf("output = %q, want the plugins directory", printed)
	}
}

// A plugin that did not load costs its own contributions and is reported on the
// same command, which is where a reader looks after noticing one is missing.
func TestPluginReportsABrokenOne(t *testing.T) {
	home := emptyHome(t)
	dir := pluginAt(t, home, "review", `{"skills": "skills", "version": "1.0"}`)
	cwd := t.TempDir()
	printed, err := runCommand(t, commandApp(t, memoryAt(t, cwd), cwd), "/plugin")
	if err != nil {
		t.Fatalf("/plugin: %v", err)
	}
	if !strings.Contains(printed, "warning: plugin: "+filepath.Join(dir, "plugin.json")) {
		t.Fatalf("output = %q, want the warning naming that file", printed)
	}
	if !strings.Contains(printed, "unknown field") {
		t.Fatalf("output = %q, want the reason", printed)
	}
}

// The command is a list with no verbs: a second argument is a misunderstanding,
// and saying so is cheaper than printing the same table again.
func TestPluginTakesNoArgument(t *testing.T) {
	emptyHome(t)
	cwd := t.TempDir()
	_, err := runCommand(t, commandApp(t, memoryAt(t, cwd), cwd), "/plugin review")
	if err == nil || !strings.Contains(err.Error(), "takes no argument") {
		t.Fatalf("err = %v, want the usage error", err)
	}
}

// The two states that must not write are the ones whose help line says which
// commands still run, and /plugin is a read of the machine: it is allowed in
// both, and the lines say so.
func TestPluginRunsInEveryState(t *testing.T) {
	if !archivedCommands["/plugin"] {
		t.Fatal("/plugin is refused in an archived session although it only reads")
	}
	if !sideCommands["/plugin"] {
		t.Fatal("/plugin is refused in a side conversation although it only reads")
	}
	for _, line := range []string{archivedHelp, sideHelp} {
		if !strings.Contains(line, "/plugin") {
			t.Fatalf("help line = %q, want it to name /plugin", line)
		}
	}
}

// The table is one shape: a plugin with nothing to say in a column gets the
// dash, which is what every other table in this program uses.
func TestPluginTableUsesADashForNothing(t *testing.T) {
	plugins := config.PluginConfig{Plugins: []config.Plugin{{Name: "quiet", Dir: "/p/quiet"}}}
	printed := &strings.Builder{}
	if err := writePlugins(printed, plugins, nil); err != nil {
		t.Fatalf("writePlugins: %v", err)
	}
	row := firstLine(printed.String()[strings.Index(printed.String(), "quiet"):])
	for _, want := range []string{"0", "-"} {
		if !strings.Contains(row, want) {
			t.Fatalf("row = %q, want %q", row, want)
		}
	}
}

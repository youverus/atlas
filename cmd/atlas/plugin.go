package main

// The /plugin command: what is installed under $ATLAS_HOME/plugins, and what
// each one adds to this session.
//
// It is a read. A plugin is not started, installed, updated or verified here —
// the only two things one can contribute are a directory of skills and a few MCP
// servers, and both are discovered by the code that would have discovered them
// anyway. There is no market and no remote install on purpose: see
// docs/architecture.md item 82 for what would have to exist first.

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/skill"
)

// pluginCommand is /plugin. It takes no argument: the list is short by
// construction (a plugin contributes at most a directory and a handful of
// servers), and a second verb would be a second thing to document for a feature
// whose whole interface is three keys in a JSON file.
func pluginCommand(application *app.App, out io.Writer) error {
	home, err := config.DefaultHome()
	if err != nil {
		return err
	}
	plugins := config.LoadPlugins(home)
	for _, problem := range plugins.Problems {
		fmt.Fprintln(out, "warning:", problem)
	}
	if len(plugins.Plugins) == 0 {
		// The path, not just "none": the question behind a bare /plugin on a
		// machine with none is "where would I put one".
		fmt.Fprintf(out, "no plugins are installed; a plugin is a directory holding a plugin.json, in:\n  %s\n", plugins.Dir)
		return nil
	}
	// One scan for the whole table, the same one the prompt block and /skill
	// use, so the count under SKILLS is what this session actually got rather
	// than what the directory holds.
	skills, notes := availableSkills(application.Session().Cwd())
	for _, note := range notes {
		fmt.Fprintln(out, "warning:", note)
	}
	return writePlugins(out, plugins, skills)
}

// writePlugins is the table /plugin prints. It is separate from the command so
// the rendering can be tested against plugins built by hand, the way /skill's
// and /mcp's tables are.
//
// SKILLS counts the skills this session sees from that plugin, which is why a
// plugin whose whole directory is shadowed by a user's own skill shows 0: the
// column is about the session, and /skill is where the shadowed one is named.
// SERVERS lists the names instead of counting them, because "did my plugin bring
// the server I am debugging" is the question the column gets asked.
func writePlugins(out io.Writer, plugins config.PluginConfig, skills []skill.Skill) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tSKILLS\tSERVERS\tDESCRIPTION\tLOCATION")
	for _, plugin := range plugins.Plugins {
		servers := "-"
		if names := plugin.ServerNames(); len(names) > 0 {
			servers = strings.Join(names, ", ")
		}
		description := plugin.Description
		if description == "" {
			description = "-"
		}
		fmt.Fprintf(writer, "%s\t%d\t%s\t%s\t%s\n",
			plugin.Name, countPluginSkills(plugin, skills), servers, oneLine(description, 80), plugin.Dir)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprint(out, "\n/plugin lists what is installed; a plugin's servers connect when atlas starts, and its skills are read again on /reload\n")
	return nil
}

// countPluginSkills is the SKILLS column: the skills of this session that came
// out of one plugin's directory.
//
// The comparison is on the directory prefix, not on the path the manifest named,
// because skill.Load resolves each root to an absolute directory before scanning
// it and a plugin's SkillsDir is already the resolved form.
func countPluginSkills(plugin config.Plugin, skills []skill.Skill) int {
	prefix := plugin.Dir + string(filepath.Separator)
	count := 0
	for _, found := range skills {
		if strings.HasPrefix(found.Path, prefix) {
			count++
		}
	}
	return count
}

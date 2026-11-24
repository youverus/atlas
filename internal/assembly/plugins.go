package assembly

import (
	"path/filepath"
	"strings"

	"pi-go/internal/config"
)

// PluginInfo is a plain metadata snapshot of one installed plugin under
// $ATLAS_HOME/plugins along with the skills and MCP servers it contributes.
type PluginInfo struct {
	Name        string
	Description string
	Dir         string
	SkillsDir   string
	SkillNames  []string
	Servers     []string
}

// PluginsSnapshot describes all plugins installed under $ATLAS_HOME/plugins
// relative to the active session's working directory.
type PluginsSnapshot struct {
	Dir     string
	Plugins []PluginInfo
	Notes   []string
}

// DescribePlugins scans installed plugins and correlates them with the active
// skills visible in cwd so shadowed skills are reflected accurately.
func DescribePlugins(cwd string) PluginsSnapshot {
	home, err := config.DefaultHome()
	if err != nil {
		return PluginsSnapshot{Notes: []string{err.Error()}}
	}
	loaded := config.LoadPlugins(home)
	notes := append([]string(nil), loaded.Problems...)
	if len(loaded.Plugins) == 0 {
		return PluginsSnapshot{
			Dir:     loaded.Dir,
			Plugins: []PluginInfo{},
			Notes:   notes,
		}
	}

	skills, skillNotes := AvailableSkills(cwd)
	notes = append(notes, skillNotes...)

	items := make([]PluginInfo, 0, len(loaded.Plugins))
	for _, plugin := range loaded.Plugins {
		prefix := plugin.Dir + string(filepath.Separator)
		var contributed []string
		for _, found := range skills {
			if strings.HasPrefix(found.Path, prefix) {
				contributed = append(contributed, found.Name)
			}
		}
		if contributed == nil {
			contributed = []string{}
		}
		servers := plugin.ServerNames()
		if servers == nil {
			servers = []string{}
		}
		items = append(items, PluginInfo{
			Name:        plugin.Name,
			Description: plugin.Description,
			Dir:         plugin.Dir,
			SkillsDir:   plugin.SkillsDir,
			SkillNames:  contributed,
			Servers:     servers,
		})
	}
	return PluginsSnapshot{
		Dir:     loaded.Dir,
		Plugins: items,
		Notes:   notes,
	}
}

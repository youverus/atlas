package webhost

import (
	"pi-go/internal/assembly"
	"pi-go/internal/httpapi"
)

// Plugins lists installed plugins under $ATLAS_HOME/plugins along with the
// skills and MCP servers each plugin contributes to the viewed session.
func (h *Host) Plugins() (httpapi.PluginState, error) {
	snap := assembly.DescribePlugins(h.skillsCwd())
	items := make([]httpapi.PluginInfo, 0, len(snap.Plugins))
	for _, p := range snap.Plugins {
		items = append(items, httpapi.PluginInfo{
			Name:        p.Name,
			Description: p.Description,
			Dir:         p.Dir,
			SkillsDir:   p.SkillsDir,
			SkillNames:  append([]string(nil), p.SkillNames...),
			Servers:     append([]string(nil), p.Servers...),
		})
	}
	return httpapi.PluginState{
		Dir:     snap.Dir,
		Plugins: items,
		Notes:   append([]string(nil), snap.Notes...),
	}, nil
}

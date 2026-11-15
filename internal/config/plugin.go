package config

// A plugin is a directory under the atlas home that declares two things: a
// directory of skills, and MCP servers. It is not a program.
//
// Nothing in this file runs, installs, downloads, unpacks, versions or verifies
// anything. The feature is the discovery of declarations a user already has on
// disk, and the interface stops there on purpose: there is no market, no remote
// install, no version or dependency resolution, and no signature, because none
// of those can be done honestly without a per-plugin permission boundary this
// version does not have (docs/architecture.md section 8, item 82).
//
// The consequence worth stating plainly: a plugin adds exactly the two things a
// reader could have added by hand. Its skills are files the model reads with the
// tool it already has, and its servers go through the same mcpEntry validation
// mcp.json does. A plugin cannot grant a capability; it can only save the typing.
//
// The package keeps the declarations unresolved. ${NAME} expansion belongs to
// LoadMCP, which is where a session's missing variables are collected once.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// PluginDirName is the directory under the atlas home that holds plugins.
	PluginDirName = "plugins"
	// pluginFileName is the file that makes a directory a plugin, the way
	// SKILL.md makes one a skill. A directory without it is not a plugin and is
	// not reported: the plugins directory is allowed to hold other things.
	pluginFileName = "plugin.json"
)

// Plugin is one installed plugin.
type Plugin struct {
	// Name is the directory name, which is the only name /plugin and the notes
	// use. There is no "name" field: two places to spell one identity is two
	// places to disagree.
	Name string
	// Dir is the plugin's own directory, absolute, because it is printed.
	Dir string
	// Description is the optional line plugin.json carries for the listing.
	Description string
	// SkillsDir is the absolute directory this plugin's skills are read from,
	// empty when it declared none or when the directory it named is not there.
	SkillsDir string

	// servers are the MCP entries the plugin declares, as the file spelled them.
	// They are unexported because a caller that wanted to start one would have
	// to reach through LoadMCP, which is the only place that validates and
	// expands them.
	servers map[string]mcpEntry
}

// ServerNames are the MCP servers this plugin declares, sorted, so a table and a
// note agree on the order.
func (p Plugin) ServerNames() []string {
	names := make([]string, 0, len(p.servers))
	for name := range p.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PluginConfig is what one machine has installed.
type PluginConfig struct {
	// Dir is where plugins live, whether or not it exists. A caller points a
	// reader at it when there are none.
	Dir string
	// Plugins are the ones that loaded, in directory order (which is name
	// order), each contributing at least one thing.
	Plugins []Plugin
	// Problems is one line per declaration that was refused or ignored.
	//
	// A broken plugin costs its own contributions and nothing else: the rest
	// still load, which is what the line buys. That is the opposite of mcp.json,
	// where one bad entry refuses the file — a plugin is one directory among
	// several, discovered rather than written, so refusing all of them for one
	// typo would be refusing the scan.
	Problems []string
}

// LoadPlugins reads the plugins under home.
//
// A missing plugins directory is not a finding: it is the state every install is
// in before the feature is used, and a warning about a directory nobody was told
// to create would train the reader to ignore the ones that matter. Every other
// failure is a line in Problems, never an error, for the reason on that field.
func LoadPlugins(home string) PluginConfig {
	cfg := PluginConfig{Dir: filepath.Join(home, PluginDirName)}
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			cfg.Problems = append(cfg.Problems, pluginProblem(cfg.Dir, "%v", err))
		}
		return cfg
	}

	// ReadDir sorts by name, which is the order /plugin prints and the order the
	// skill roots are listed in. Nothing here sorts again.
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(cfg.Dir, name)
		// Stat follows symlinks, the same way skill discovery does: pointing a
		// plugin directory at a checkout is how a person develops one.
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		if !validMCPName(name) {
			cfg.Problems = append(cfg.Problems, pluginProblem(dir, "the directory name must be letters, digits, \"-\" and \"_\"; it is the name /plugin lists and a note quotes"))
			continue
		}
		found, problems := loadPlugin(name, dir)
		cfg.Problems = append(cfg.Problems, problems...)
		if found != nil {
			cfg.Plugins = append(cfg.Plugins, *found)
		}
	}
	return cfg
}

// loadPlugin reads one directory. A nil plugin means "not a plugin, or not one
// this version can use", and problems says why when there is something to say.
func loadPlugin(name, dir string) (*Plugin, []string) {
	path := filepath.Join(dir, pluginFileName)
	file, err := readPluginFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// A directory without a plugin.json is not a plugin. Saying so on every
		// start would make an unrelated file in the directory a warning.
		return nil, nil
	}
	if err != nil {
		// The error already names the file.
		return nil, []string{err.Error()}
	}

	found := &Plugin{Name: name, Dir: dir, Description: strings.TrimSpace(file.Description)}
	problems := make([]string, 0, 2)

	if file.Skills != "" {
		skillsDir, err := pluginPath(dir, file.Skills)
		if err != nil {
			return nil, []string{pluginProblem(path, "skills: %v", err)}
		}
		switch info, err := os.Stat(skillsDir); {
		case err != nil || !info.IsDir():
			// The declaration is kept as a problem rather than a refusal: the
			// servers half may still work, and "I deleted the directory" is a
			// likelier story than "I meant something else".
			problems = append(problems, pluginProblem(path, "skills: %q is not a directory, so this plugin contributes no skills", file.Skills))
		default:
			found.SkillsDir = skillsDir
		}
	}

	servers, err := pluginServers(path, file.Servers)
	if err != nil {
		return nil, []string{err.Error()}
	}
	found.servers = servers

	if found.SkillsDir == "" && len(found.servers) == 0 {
		problems = append(problems, pluginProblem(path, "declares neither \"skills\" nor \"mcpServers\", so it contributes nothing; a plugin that adds nothing is more likely a half-finished file than one worth loading silently"))
		return nil, problems
	}
	return found, problems
}

// pluginServers validates the declared servers.
//
// Validation is the file's, not the connection's: a plugin with a server that
// names both transports is refused here, on the line that was written, instead
// of becoming a process that fails to start two commands later.
func pluginServers(path string, entries map[string]mcpEntry) (map[string]mcpEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	// The "-"/"_" check is per plugin, because that is where the tool-name
	// prefix comes from: two entries in one plugin would produce the same
	// mcp__ name, and the second would shadow the first. A clash with another
	// plugin or with mcp.json is not decided here — LoadMCP owns that order.
	canonical := make(map[string]string, len(names))
	for _, name := range names {
		key := strings.ReplaceAll(name, "-", "_")
		if previous, seen := canonical[key]; seen {
			return nil, fmt.Errorf("plugin: %s: mcpServers: %q and %q differ only in \"-\" and \"_\"", path, previous, name)
		}
		canonical[key] = name
		if err := entries[name].validate(name); err != nil {
			return nil, fmt.Errorf("plugin: %s: mcpServers.%s: %w", path, name, err)
		}
	}
	return entries, nil
}

// pluginPath resolves the directory a plugin declares inside itself.
//
// The check is lexical and happens before the filesystem is asked anything: a
// manifest that points outside its own directory is not a plugin with an unusual
// layout, it is a file that can make one plugin hand over another plugin's
// skills, and then "what does this plugin contribute" has no answer that is true
// for both. Symlinks are not resolved here for the same reason skill discovery
// does not resolve them: pointing a directory somewhere else is the user's own
// arrangement, made in a place they can see.
func pluginPath(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q is an absolute path; a plugin declares a directory inside itself", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the plugin directory", rel)
	}
	return filepath.Join(dir, cleaned), nil
}

// pluginFile is the JSON shape of plugin.json, which is the whole interface.
//
// Two keys is not an accident of the first version. "description" is for the
// person reading /plugin, "skills" names a directory of SKILL.md files, and
// "mcpServers" is the same object mcp.json holds, so a plugin author writes one
// format they already know. Anything else a plugin format usually has — a
// version, a range, a dependency, an entry point, a hash — would be a promise
// this version cannot keep, and an unknown key is refused rather than ignored so
// an author finds that out here.
type pluginFile struct {
	Description string              `json:"description"`
	Skills      string              `json:"skills"`
	Servers     map[string]mcpEntry `json:"mcpServers"`
}

// readPluginFile decodes one plugin.json strictly: unknown keys refused, one
// value only, and a size limit. It is the rule config.json, tui.json and
// mcp.json already follow.
func readPluginFile(path string) (pluginFile, error) {
	handle, err := os.Open(path)
	if err != nil {
		return pluginFile{}, err
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		return pluginFile{}, err
	}
	if info.Size() > maxSize {
		return pluginFile{}, fmt.Errorf("plugin: %s: %d bytes exceeds the %d byte limit", path, info.Size(), maxSize)
	}

	decoder := json.NewDecoder(io.LimitReader(handle, maxSize))
	decoder.DisallowUnknownFields()
	var file pluginFile
	if err := decoder.Decode(&file); err != nil {
		return pluginFile{}, fmt.Errorf("plugin: %s: %w", path, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return pluginFile{}, fmt.Errorf("plugin: %s: trailing data after the plugin object", path)
	}
	return file, nil
}

// pluginProblem formats one warning. The prefix is the shape skill and mcp use:
// a reader with several directories needs to know which file to fix.
func pluginProblem(path, format string, args ...any) string {
	return "plugin: " + path + ": " + fmt.Sprintf(format, args...)
}

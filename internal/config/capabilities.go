package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
)

const capabilitiesFileName = "capabilities.json"

// CapabilitySettings stores the local user's Skill and MCP switches.
type CapabilitySettings struct {
	DisabledSkills map[string]bool `json:"disabledSkills,omitempty"`
	MCP            map[string]bool `json:"mcp,omitempty"`
}

var capabilitySettingsMu sync.Mutex

// LoadCapabilities reads the user's switches. A missing file uses the defaults.
func LoadCapabilities(home string) (CapabilitySettings, error) {
	path := filepath.Join(home, capabilitiesFileName)
	raw, err := readRawDoc(path)
	if err != nil {
		return CapabilitySettings{}, err
	}
	settings := CapabilitySettings{}
	if value, ok := raw["disabledSkills"]; ok {
		if err := json.Unmarshal(value, &settings.DisabledSkills); err != nil {
			return CapabilitySettings{}, fmt.Errorf("config: %s: disabledSkills: %w", path, err)
		}
	}
	if value, ok := raw["mcp"]; ok {
		if err := json.Unmarshal(value, &settings.MCP); err != nil {
			return CapabilitySettings{}, fmt.Errorf("config: %s: mcp: %w", path, err)
		}
	}
	return settings, nil
}

// SaveSkillEnabled stores the setting for a Skill name across all discovery roots.
func SaveSkillEnabled(home, name string, enabled bool) error {
	return saveCapability(home, "disabledSkills", func(values map[string]bool) {
		if enabled {
			delete(values, name)
		} else {
			values[name] = true
		}
	})
}

// SaveMCPEnabled stores the user's override for a configured MCP server.
func SaveMCPEnabled(home, name string, enabled bool) error {
	return saveCapability(home, "mcp", func(values map[string]bool) { values[name] = enabled })
}

func saveCapability(home, key string, update func(map[string]bool)) error {
	capabilitySettingsMu.Lock()
	defer capabilitySettingsMu.Unlock()

	path := filepath.Join(home, capabilitiesFileName)
	raw, err := readRawDoc(path)
	if err != nil {
		return err
	}
	var values map[string]bool
	if value, ok := raw[key]; ok {
		if err := json.Unmarshal(value, &values); err != nil {
			return fmt.Errorf("config: %s: %s: %w", path, key, err)
		}
	}
	if values == nil {
		values = make(map[string]bool)
	}
	update(values)
	if len(values) == 0 {
		delete(raw, key)
	} else {
		data, err := json.Marshal(values)
		if err != nil {
			return err
		}
		raw[key] = data
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data)+1) > maxSize {
		return fmt.Errorf("config: %s: settings exceed the %d byte limit", path, maxSize)
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

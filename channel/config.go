package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"pi-go/internal/config"
)

// ChannelsFile is the hand-edited per-platform configuration inside the atlas
// home. It stays out of config.json on purpose: the gateway owns it, pairing
// approval only ever writes the runtime allowlist beside it, and a reader who
// hand-edits config.json should never have a gateway reformatting it.
const ChannelsFile = "channels.json"

// ChannelsPath is where the gateway reads its configuration.
func ChannelsPath() (string, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ChannelsFile), nil
}

// DataPath is the directory for gateway runtime state: bindings, the dedupe
// ring, pairing codes and the runtime allowlist.
func DataPath() (string, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "channels"), nil
}

// Section is one adapter's raw configuration. The gateway treats platform
// bodies as opaque bytes and hands each to its own package: channel never
// learns what a Feishu field is called.
type Section map[string]json.RawMessage

// LoadSections reads channels.json. A missing file returns (nil, nil): the
// caller turns that into its own first-run message.
func LoadSections(path string) (Section, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sections Section
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("channels %s: %w", path, err)
	}
	return sections, nil
}

// WriteTemplate creates a starter channels.json with every field empty. It
// refuses to overwrite: an existing file is someone's configuration.
func WriteTemplate(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	template := `{
  "feishu": {
    "appId": "",
    "appSecret": "env:ATLAS_FEISHU_APP_SECRET",
    "dmPolicy": "pairing",
    "groupPolicy": "allowlist",
    "requireMention": true,
    "admins": [],
    "cwd": ""
  }
}
`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(template), 0o600)
}

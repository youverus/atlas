// Package feishu is atlas's Feishu/Lark adapter: the official SDK's WebSocket
// long connection on the inbound side, typed REST calls on the outbound one.
//
// The transport decisions this package makes are the ones channel-feishu.md
// records: the SDK's event handler must return inside the platform's three
// second window, so events are queued and acked, not processed inline; and
// every REST failure is reported, never swallowed, because a silent adapter
// is one nobody can debug from the chat.
package feishu

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config is one feishu entry in channels.json. Field names match the doc's
// table; the defaults are the doc's defaults.
type Config struct {
	Enabled   bool   `json:"enabled,omitempty"`
	AppID     string `json:"appId"`
	AppSecret string `json:"appSecret"`

	DMPolicy       string               `json:"dmPolicy"`     // pairing | allowlist | open | disabled
	GroupPolicy    string               `json:"groupPolicy"`  // allowlist | open | admin_only | disabled
	AllowedUsers   []string             `json:"allowedUsers"` // open_id / union_id
	Groups         map[string]GroupRule `json:"groups"`       // per-chat overrides, keyed by chat_id
	Admins         []string             `json:"admins"`       // open_id
	RequireMention *bool                `json:"requireMention"`
	AllowBots      bool                 `json:"allowBots"`
	CWD            string               `json:"cwd"`
}

// GroupRule overrides the gateway-wide group policy for one chat.
type GroupRule struct {
	Policy         string   `json:"policy"`
	AllowedUsers   []string `json:"allowedUsers"`
	RequireMention *bool    `json:"requireMention"`
}

// ParseRaw decodes one channels.json section and applies defaults without
// validating credentials or expanding env: secrets, so settings UIs can read
// partial or env-backed configs safely.
func ParseRaw(raw json.RawMessage) (Config, error) {
	cfg := Config{DMPolicy: "pairing", GroupPolicy: "allowlist", RequireMention: boolPtr(true)}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("feishu config: %w", err)
		}
	}
	if cfg.DMPolicy == "" {
		cfg.DMPolicy = "pairing"
	}
	if cfg.GroupPolicy == "" {
		cfg.GroupPolicy = "allowlist"
	}
	if cfg.RequireMention == nil {
		cfg.RequireMention = boolPtr(true)
	}
	return cfg, nil
}

// Parse decodes one channels.json section, applies defaults and the two env
// overrides, and resolves the "env:VAR" secret syntax.
func Parse(raw json.RawMessage) (Config, error) {
	cfg, err := ParseRaw(raw)
	if err != nil {
		return cfg, err
	}
	if v := strings.TrimSpace(os.Getenv("ATLAS_FEISHU_APP_ID")); v != "" {
		cfg.AppID = v
	}
	if v := strings.TrimSpace(os.Getenv("ATLAS_FEISHU_APP_SECRET")); v != "" {
		cfg.AppSecret = v
	}
	secret, err := resolveSecret(cfg.AppSecret)
	if err != nil {
		return cfg, err
	}
	cfg.AppSecret = secret
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return cfg, fmt.Errorf("feishu config: appId 或 appSecret 缺失")
	}
	return cfg, nil
}

// resolveSecret reads the "env:VAR" indirection. A literal secret in the file
// passes through unchanged; the file's owner chose that, and the gateway's
// job is logs, not policing.
func resolveSecret(value string) (string, error) {
	value = strings.TrimSpace(value)
	if after, ok := strings.CutPrefix(value, "env:"); ok {
		name := strings.TrimSpace(after)
		resolved := strings.TrimSpace(os.Getenv(name))
		if resolved == "" {
			return "", fmt.Errorf("feishu config: 环境变量 %s 未设置", name)
		}
		return resolved, nil
	}
	return value, nil
}

func boolPtr(v bool) *bool { return &v }

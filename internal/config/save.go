package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SaveProviders updates defaultModel, thinkingLevel, and providers in path while
// preserving unrelated top-level settings (such as tui, web, sandbox, permission,
// goal, update, and sessionDir). The resulting document is strictly decoded and
// validated before overwriting path atomically.
func SaveProviders(
	path string,
	defaultModel string,
	thinkingLevel string,
	providers map[string]Provider,
	extraValidate func(*Config) error,
) error {
	return SaveModelSettings(path, defaultModel, thinkingLevel, providers, nil, extraValidate)
}

// SaveModelSettings also updates subagent settings when supplied by the editor.
func SaveModelSettings(
	path string,
	defaultModel string,
	thinkingLevel string,
	providers map[string]Provider,
	subagents *Subagents,
	extraValidate func(*Config) error,
) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config: empty config path")
	}

	rawDoc, err := readRawDoc(path)
	if err != nil {
		return err
	}

	defaultModel = strings.TrimSpace(defaultModel)
	thinkingLevel = strings.TrimSpace(thinkingLevel)

	if defaultModel != "" {
		encoded, err := json.Marshal(defaultModel)
		if err != nil {
			return fmt.Errorf("config: encode defaultModel: %w", err)
		}
		rawDoc["defaultModel"] = encoded
	} else {
		delete(rawDoc, "defaultModel")
	}

	if thinkingLevel != "" {
		encoded, err := json.Marshal(thinkingLevel)
		if err != nil {
			return fmt.Errorf("config: encode thinkingLevel: %w", err)
		}
		rawDoc["thinkingLevel"] = encoded
	} else {
		delete(rawDoc, "thinkingLevel")
	}

	cleanProviders := make(map[string]saveProvider, len(providers))
	for name, prov := range providers {
		cleanProviders[name] = toSaveProvider(prov)
	}
	encodedProviders, err := json.Marshal(cleanProviders)
	if err != nil {
		return fmt.Errorf("config: encode providers: %w", err)
	}
	rawDoc["providers"] = encodedProviders

	if subagents != nil {
		if subagents.Default.Model == "" && len(subagents.Roles) == 0 {
			delete(rawDoc, "subagents")
		} else {
			encoded, err := json.Marshal(subagents)
			if err != nil {
				return fmt.Errorf("config: encode subagents: %w", err)
			}
			rawDoc["subagents"] = encoded
		}
	}

	out, err := json.MarshalIndent(rawDoc, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode %s: %w", path, err)
	}
	out = append(out, '\n')
	if int64(len(out)) > maxSize {
		return fmt.Errorf("config: %s: %d bytes exceeds the %d byte limit", path, len(out), maxSize)
	}

	cfg, err := decodeConfig(bytes.NewReader(out), path)
	if err != nil {
		return err
	}
	if _, err := cfg.Resolve(""); err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	if extraValidate != nil {
		if err := extraValidate(cfg); err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
	}

	return writeFileAtomic(path, out, 0o600)
}

func readRawDoc(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("config: %s: %d bytes exceeds the %d byte limit", path, len(data), maxSize)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: chmod %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	cleanup = false
	return nil
}

type saveProvider struct {
	API       string      `json:"api"`
	BaseURL   string      `json:"baseUrl,omitempty"`
	APIKey    string      `json:"apiKey,omitempty"`
	APIKeyEnv string      `json:"apiKeyEnv,omitempty"`
	UserAgent string      `json:"userAgent,omitempty"`
	Compat    *saveCompat `json:"compat,omitempty"`
	Models    []saveModel `json:"models"`
}

type saveCompat struct {
	MaxTokensField                  string `json:"maxTokensField,omitempty"`
	RequireReasoningContent         bool   `json:"requireReasoningContent,omitempty"`
	DisableStreamOptions            bool   `json:"disableStreamOptions,omitempty"`
	TolerateMissingFinishReason     bool   `json:"tolerateMissingFinishReason,omitempty"`
	RequireToolResultName           bool   `json:"requireToolResultName,omitempty"`
	RequireAssistantAfterToolResult bool   `json:"requireAssistantAfterToolResult,omitempty"`
	Version                         string `json:"version,omitempty"`
	ThinkingDialect                 string `json:"thinkingDialect,omitempty"`
	DisableReasoningEffort          bool   `json:"disableReasoningEffort,omitempty"`
	SendStore                       bool   `json:"sendStore,omitempty"`
	ToolChoice                      string `json:"toolChoice,omitempty"`
	AdaptiveThinking                bool   `json:"adaptiveThinking,omitempty"`
	StrictTools                     bool   `json:"strictTools,omitempty"`
	ServiceTier                     string `json:"serviceTier,omitempty"`
	Verbosity                       string `json:"verbosity,omitempty"`
	Truncation                      string `json:"truncation,omitempty"`
}

type saveModel struct {
	ID             string   `json:"id"`
	Name           string   `json:"name,omitempty"`
	BaseURL        string   `json:"baseUrl,omitempty"`
	ContextWindow  int      `json:"contextWindow,omitempty"`
	MaxTokens      int      `json:"maxTokens,omitempty"`
	Reasoning      bool     `json:"reasoning,omitempty"`
	ThinkingLevels []string `json:"thinkingLevels,omitempty"`
	Input          []string `json:"input,omitempty"`
	UserAgent      string   `json:"userAgent,omitempty"`
}

func toSaveProvider(p Provider) saveProvider {
	models := make([]saveModel, 0, len(p.Models))
	for _, m := range p.Models {
		var levels []string
		if len(m.ThinkingLevels) > 0 {
			levels = append([]string(nil), m.ThinkingLevels...)
		}
		var input []string
		if len(m.Input) > 0 {
			input = append([]string(nil), m.Input...)
		}
		models = append(models, saveModel{
			ID:             strings.TrimSpace(m.ID),
			Name:           strings.TrimSpace(m.Name),
			BaseURL:        strings.TrimSpace(m.BaseURL),
			ContextWindow:  m.ContextWindow,
			MaxTokens:      m.MaxTokens,
			Reasoning:      m.Reasoning,
			ThinkingLevels: levels,
			Input:          input,
			UserAgent:      strings.TrimSpace(m.UserAgent),
		})
	}
	var compat *saveCompat
	if p.Compat != (Compat{}) {
		c := saveCompat{
			MaxTokensField:                  strings.TrimSpace(p.Compat.MaxTokensField),
			RequireReasoningContent:         p.Compat.RequireReasoningContent,
			DisableStreamOptions:            p.Compat.DisableStreamOptions,
			TolerateMissingFinishReason:     p.Compat.TolerateMissingFinishReason,
			RequireToolResultName:           p.Compat.RequireToolResultName,
			RequireAssistantAfterToolResult: p.Compat.RequireAssistantAfterToolResult,
			Version:                         strings.TrimSpace(p.Compat.Version),
			ThinkingDialect:                 strings.TrimSpace(p.Compat.ThinkingDialect),
			DisableReasoningEffort:          p.Compat.DisableReasoningEffort,
			SendStore:                       p.Compat.SendStore,
			ToolChoice:                      strings.TrimSpace(p.Compat.ToolChoice),
			AdaptiveThinking:                p.Compat.AdaptiveThinking,
			StrictTools:                     p.Compat.StrictTools,
			ServiceTier:                     strings.TrimSpace(p.Compat.ServiceTier),
			Verbosity:                       strings.TrimSpace(p.Compat.Verbosity),
			Truncation:                      strings.TrimSpace(p.Compat.Truncation),
		}
		if c != (saveCompat{}) {
			compat = &c
		}
	}
	return saveProvider{
		API:       strings.TrimSpace(p.API),
		BaseURL:   strings.TrimSpace(p.BaseURL),
		APIKey:    strings.TrimSpace(p.APIKey),
		APIKeyEnv: strings.TrimSpace(p.APIKeyEnv),
		UserAgent: strings.TrimSpace(p.UserAgent),
		Compat:    compat,
		Models:    models,
	}
}

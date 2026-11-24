package httpapi

import (
	"encoding/json"
	"net/http"
)

// ModelConfigEditor is an optional extension of Host for reading and overwriting
// the provider/model declarations in config.json from the settings UI. Builds
// without one answer 501.
type ModelConfigEditor interface {
	ModelConfig() (ModelConfig, error)
	SaveModelConfig(ModelConfig) error
}

// ModelConfig mirrors the editable model-related top-level fields of config.json.
type ModelConfig struct {
	ConfigPath    string           `json:"configPath,omitempty"`
	DefaultModel  string           `json:"defaultModel"`
	ThinkingLevel string           `json:"thinkingLevel,omitempty"`
	Providers     []ProviderConfig `json:"providers"`
	Subagents     *SubagentModels  `json:"subagents,omitempty"`
}

// SubagentModels carries the model choices exposed in settings.
type SubagentModels struct {
	DefaultModel string `json:"defaultModel,omitempty"`
	Explorer     string `json:"explorer,omitempty"`
	Verifier     string `json:"verifier,omitempty"`
}

// ProviderConfig is one named provider endpoint and the models it declares.
type ProviderConfig struct {
	Name         string            `json:"name"`
	API          string            `json:"api"`
	BaseURL      string            `json:"baseUrl,omitempty"`
	APIKey       string            `json:"apiKey,omitempty"`
	APIKeyEnv    string            `json:"apiKeyEnv,omitempty"`
	APIKeyEnvSet bool              `json:"apiKeyEnvSet,omitempty"`
	UserAgent    string            `json:"userAgent,omitempty"`
	Compat       *CompatConfig     `json:"compat,omitempty"`
	Models       []ModelItemConfig `json:"models"`
}

// CompatConfig mirrors config.Compat for round-tripping per-provider protocol switches.
type CompatConfig struct {
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

// ModelItemConfig is one editable model entry inside ProviderConfig.Models.
type ModelItemConfig struct {
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

func normalizeModelConfig(cfg ModelConfig) ModelConfig {
	if cfg.Providers == nil {
		cfg.Providers = []ProviderConfig{}
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].Models == nil {
			cfg.Providers[i].Models = []ModelItemConfig{}
		}
	}
	return cfg
}

func (s *Server) serveModelConfig(w http.ResponseWriter, _ *http.Request) {
	if !s.needHost(w) {
		return
	}
	editor, ok := s.host.(ModelConfigEditor)
	if !ok {
		http.Error(w, "httpapi: this build cannot edit model configuration", http.StatusNotImplemented)
		return
	}
	cfg, err := editor.ModelConfig()
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, normalizeModelConfig(cfg))
}

func (s *Server) serveModelConfigSave(w http.ResponseWriter, r *http.Request) {
	if !s.needHost(w) {
		return
	}
	editor, ok := s.host.(ModelConfigEditor)
	if !ok {
		http.Error(w, "httpapi: this build cannot edit model configuration", http.StatusNotImplemented)
		return
	}
	var request ModelConfig
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := editor.SaveModelConfig(request); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	s.publishStatus(s.currentStatusEvent())
	writeJSON(w, map[string]bool{"ok": true})
}

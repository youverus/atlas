package webhost

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/httpapi"
	"pi-go/internal/wire"
)

// ModelConfig reads the active config.json and projects its editable model and
// provider declarations for the settings UI.
func (h *Host) ModelConfig() (httpapi.ModelConfig, error) {
	if h == nil || h.kernel == nil {
		return httpapi.ModelConfig{}, errors.New("webhost: kernel is not initialized")
	}
	path, err := h.kernel.ConfigPath()
	if err != nil {
		return httpapi.ModelConfig{}, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && h.kernel.Config != nil {
			cfg = h.kernel.Config
		} else {
			return httpapi.ModelConfig{}, err
		}
	}
	return toHTTPModelConfig(path, cfg), nil
}

// SaveModelConfig validates the edited provider and model declarations, overwrites
// config.json atomically while keeping unrelated sections intact, and hot-reloads
// all live sessions.
func (h *Host) SaveModelConfig(next httpapi.ModelConfig) error {
	if h == nil || h.kernel == nil {
		return errors.New("webhost: kernel is not initialized")
	}
	path, err := h.kernel.ConfigPath()
	if err != nil {
		return err
	}

	providers := make(map[string]config.Provider, len(next.Providers))
	for index, entry := range next.Providers {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return fmt.Errorf("providers[%d]: provider name is required", index)
		}
		if _, dup := providers[name]; dup {
			return fmt.Errorf("providers.%s: duplicate provider name", name)
		}
		providers[name] = fromHTTPProvider(entry)
	}

	var subagents *config.Subagents
	if next.Subagents != nil {
		var previous config.Subagents
		if current, err := config.Load(path); err == nil && current.Subagents != nil {
			previous = *current.Subagents
		}
		selectModel := func(model string, old config.SubagentModel) config.SubagentModel {
			model = strings.TrimSpace(model)
			if old.Model == model {
				return old
			}
			return config.SubagentModel{Model: model}
		}
		subagents = &config.Subagents{Default: selectModel(next.Subagents.DefaultModel, previous.Default), Roles: make(map[string]config.SubagentModel)}
		for role, model := range map[string]string{wire.SubagentExplorer: next.Subagents.Explorer, wire.SubagentVerifier: next.Subagents.Verifier} {
			if setting := selectModel(model, previous.Roles[role]); setting.Model != "" {
				subagents.Roles[role] = setting
			}
		}
	}
	prevData, prevErr := os.ReadFile(path)
	if err := config.SaveModelSettings(path, next.DefaultModel, next.ThinkingLevel, providers, subagents, assembly.ValidateSelections); err != nil {
		return err
	}

	if err := h.ReloadConfig(); err != nil {
		if prevErr == nil {
			_ = os.WriteFile(path, prevData, 0o600)
		}
		return err
	}
	return nil
}

func toHTTPModelConfig(path string, cfg *config.Config) httpapi.ModelConfig {
	out := httpapi.ModelConfig{
		ConfigPath: path,
		Providers:  []httpapi.ProviderConfig{},
		Subagents:  &httpapi.SubagentModels{},
	}
	if cfg == nil {
		return out
	}
	if cfg.Subagents != nil {
		out.Subagents = &httpapi.SubagentModels{
			DefaultModel: cfg.Subagents.Default.Model,
			Explorer:     cfg.Subagents.Roles[wire.SubagentExplorer].Model,
			Verifier:     cfg.Subagents.Roles[wire.SubagentVerifier].Model,
		}
	}
	out.DefaultModel = cfg.DefaultModel
	out.ThinkingLevel = cfg.ThinkingLevel

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	out.Providers = make([]httpapi.ProviderConfig, 0, len(names))
	for _, name := range names {
		prov := cfg.Providers[name]
		models := make([]httpapi.ModelItemConfig, 0, len(prov.Models))
		for _, m := range prov.Models {
			models = append(models, httpapi.ModelItemConfig{
				ID:             m.ID,
				Name:           m.Name,
				BaseURL:        m.BaseURL,
				ContextWindow:  m.ContextWindow,
				MaxTokens:      m.MaxTokens,
				Reasoning:      m.Reasoning,
				ThinkingLevels: append([]string(nil), m.ThinkingLevels...),
				Input:          append([]string(nil), m.Input...),
				UserAgent:      m.UserAgent,
			})
		}
		var compat *httpapi.CompatConfig
		if prov.Compat != (config.Compat{}) {
			c := toHTTPCompat(prov.Compat)
			compat = &c
		}
		envName := strings.TrimSpace(prov.APIKeyEnv)
		envSet := envName != "" && strings.TrimSpace(os.Getenv(envName)) != ""
		out.Providers = append(out.Providers, httpapi.ProviderConfig{
			Name:         name,
			API:          prov.API,
			BaseURL:      prov.BaseURL,
			APIKey:       prov.APIKey,
			APIKeyEnv:    prov.APIKeyEnv,
			APIKeyEnvSet: envSet,
			UserAgent:    prov.UserAgent,
			Compat:       compat,
			Models:       models,
		})
	}
	return out
}

func fromHTTPProvider(entry httpapi.ProviderConfig) config.Provider {
	models := make([]config.Model, 0, len(entry.Models))
	for _, m := range entry.Models {
		models = append(models, config.Model{
			ID:             strings.TrimSpace(m.ID),
			Name:           strings.TrimSpace(m.Name),
			BaseURL:        strings.TrimSpace(m.BaseURL),
			ContextWindow:  m.ContextWindow,
			MaxTokens:      m.MaxTokens,
			Reasoning:      m.Reasoning,
			ThinkingLevels: trimNonEmpty(m.ThinkingLevels),
			Input:          trimNonEmpty(m.Input),
			UserAgent:      strings.TrimSpace(m.UserAgent),
		})
	}
	var compat config.Compat
	if entry.Compat != nil {
		compat = fromHTTPCompat(*entry.Compat)
	}
	return config.Provider{
		API:       strings.TrimSpace(entry.API),
		BaseURL:   strings.TrimSpace(entry.BaseURL),
		APIKey:    strings.TrimSpace(entry.APIKey),
		APIKeyEnv: strings.TrimSpace(entry.APIKeyEnv),
		UserAgent: strings.TrimSpace(entry.UserAgent),
		Compat:    compat,
		Models:    models,
	}
}

func toHTTPCompat(c config.Compat) httpapi.CompatConfig {
	return httpapi.CompatConfig{
		MaxTokensField:                  c.MaxTokensField,
		RequireReasoningContent:         c.RequireReasoningContent,
		DisableStreamOptions:            c.DisableStreamOptions,
		TolerateMissingFinishReason:     c.TolerateMissingFinishReason,
		RequireToolResultName:           c.RequireToolResultName,
		RequireAssistantAfterToolResult: c.RequireAssistantAfterToolResult,
		Version:                         c.Version,
		ThinkingDialect:                 c.ThinkingDialect,
		DisableReasoningEffort:          c.DisableReasoningEffort,
		SendStore:                       c.SendStore,
		ToolChoice:                      c.ToolChoice,
		AdaptiveThinking:                c.AdaptiveThinking,
		StrictTools:                     c.StrictTools,
		ServiceTier:                     c.ServiceTier,
		Verbosity:                       c.Verbosity,
		Truncation:                      c.Truncation,
	}
}

func fromHTTPCompat(c httpapi.CompatConfig) config.Compat {
	return config.Compat{
		MaxTokensField:                  strings.TrimSpace(c.MaxTokensField),
		RequireReasoningContent:         c.RequireReasoningContent,
		DisableStreamOptions:            c.DisableStreamOptions,
		TolerateMissingFinishReason:     c.TolerateMissingFinishReason,
		RequireToolResultName:           c.RequireToolResultName,
		RequireAssistantAfterToolResult: c.RequireAssistantAfterToolResult,
		Version:                         strings.TrimSpace(c.Version),
		ThinkingDialect:                 strings.TrimSpace(c.ThinkingDialect),
		DisableReasoningEffort:          c.DisableReasoningEffort,
		SendStore:                       c.SendStore,
		ToolChoice:                      strings.TrimSpace(c.ToolChoice),
		AdaptiveThinking:                c.AdaptiveThinking,
		StrictTools:                     c.StrictTools,
		ServiceTier:                     strings.TrimSpace(c.ServiceTier),
		Verbosity:                       strings.TrimSpace(c.Verbosity),
		Truncation:                      strings.TrimSpace(c.Truncation),
	}
}

func trimNonEmpty(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

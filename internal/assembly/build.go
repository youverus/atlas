package assembly

import (
	"fmt"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/google"
	"pi-go/internal/provider/openai"
	"pi-go/internal/provider/responses"
	"pi-go/internal/wire"
)

// Build turns one config selection into the descriptor the kernel records and
// the adapter that streams it.
//
// Both halves come from one place on purpose: a session stores provider and
// model names, and a replay rebuilds the descriptor from them. If the initial
// model and the resolved model were built by different code they would drift,
// and a resumed session would send a different request than the one it
// recorded.
func Build(selection config.Selection) (wire.Model, provider.Provider, error) {
	adapter, err := adapterOf(selection)
	if err != nil {
		return wire.Model{}, nil, err
	}
	return ModelOf(selection), adapter, nil
}

// ModelOf converts a declaration into the kernel descriptor.
//
// BaseURL is folded to the effective endpoint: the model override wins, the
// provider root fills the rest. The descriptor is what lands in assistant
// messages, so it records the endpoint that was actually called rather than the
// one that would have been the default.
//
// The numbers come from FactsOf, which is what lets a config declare a model by
// id alone: a window it did not write is the vendor's documented window, and a
// window of zero would turn automatic compaction off without saying so.
func ModelOf(selection config.Selection) wire.Model {
	declared := selection.Model
	facts := FactsOf(selection)
	return wire.Model{
		ID:            declared.ID,
		Name:          facts.Name,
		API:           selection.Provider.API,
		Provider:      selection.ProviderName,
		BaseURL:       EffectiveBaseURL(selection),
		Reasoning:     declared.Reasoning,
		ContextWindow: facts.ContextWindow,
		MaxTokens:     facts.MaxTokens,
		Input:         append([]string(nil), facts.Input...),
	}
}

// EffectiveBaseURL is the endpoint a selection actually calls.
func EffectiveBaseURL(selection config.Selection) string {
	if selection.Model.BaseURL != "" {
		return selection.Model.BaseURL
	}
	return selection.Provider.BaseURL
}

// adapterOf builds the wire adapter and attaches the credential.
//
// The key is attached here, not in the kernel: the agent builds provider.Options
// with only the thinking level, which keeps a secret out of the transcript and
// out of every session file. Wrapping the adapter is the only place that can
// see both the key and the request.
func adapterOf(selection config.Selection) (provider.Provider, error) {
	baseURL := EffectiveBaseURL(selection)
	key := selection.Provider.Key()
	compat := selection.Provider.Compat

	switch selection.Provider.API {
	case openai.APIName:
		adapter := openai.New(openai.Compat{
			BaseURL:                         baseURL,
			MaxTokensField:                  compat.MaxTokensField,
			RequireReasoningContent:         compat.RequireReasoningContent,
			DisableStreamOptions:            compat.DisableStreamOptions,
			TolerateMissingFinishReason:     compat.TolerateMissingFinishReason,
			RequireToolResultName:           compat.RequireToolResultName,
			RequireAssistantAfterToolResult: compat.RequireAssistantAfterToolResult,
			ThinkingDialect:                 compat.ThinkingDialect,
			DisableReasoningEffort:          compat.DisableReasoningEffort,
			ToolChoice:                      compat.ToolChoice,
			SendStore:                       compat.SendStore,
		})
		return provider.WithAPIKey(adapter, key), nil
	case anthropic.APIName:
		adapter := anthropic.New(anthropic.Compat{
			BaseURL:          baseURL,
			Version:          compat.Version,
			ThinkingDialect:  compat.ThinkingDialect,
			AdaptiveThinking: compat.AdaptiveThinking,
			StrictTools:      compat.StrictTools,
			ToolChoice:       compat.ToolChoice,
		})
		return provider.WithAPIKey(adapter, key), nil
	case responses.APIName:
		adapter := responses.New(responses.Compat{
			BaseURL:     baseURL,
			ToolChoice:  compat.ToolChoice,
			ServiceTier: compat.ServiceTier,
			Verbosity:   compat.Verbosity,
			Truncation:  compat.Truncation,
		})
		return provider.WithAPIKey(adapter, key), nil
	case google.APIName:
		adapter := google.New(google.Compat{
			BaseURL:         baseURL,
			ThinkingDialect: compat.ThinkingDialect,
			ToolChoice:      compat.ToolChoice,
		})
		return provider.WithAPIKey(adapter, key), nil
	}
	return nil, fmt.Errorf("provider %q declares api %q; this binary speaks %s, %s, %s and %s",
		selection.ProviderName, selection.Provider.API,
		openai.APIName, anthropic.APIName, responses.APIName, google.APIName)
}

// ResolverFor builds the function the kernel calls when a session records a
// model this process did not start with, and on every /model switch.
func ResolverFor(cfg *config.Config) app.Resolver {
	return func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		selection, err := cfg.Lookup(providerName, modelID)
		if err != nil {
			return wire.Model{}, nil, err
		}
		return Build(selection)
	}
}

// CredentialNotice explains a missing credential instead of failing on it: a
// local endpoint needs no key, and the error that arrives from the first
// request would name the wrong problem.
func CredentialNotice(selection config.Selection) string {
	if selection.Provider.Key() != "" {
		return ""
	}
	hint := "set apiKey"
	if selection.Provider.APIKeyEnv != "" {
		hint = "export " + selection.Provider.APIKeyEnv + " or set apiKey"
	}
	return fmt.Sprintf("provider %q has no credential; %s under providers.%s", selection.ProviderName, hint, selection.ProviderName)
}

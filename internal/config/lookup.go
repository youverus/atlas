package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Selection is one declared model together with the provider that serves it.
// It is what a caller needs to build both a wire descriptor and an adapter.
type Selection struct {
	ProviderName string
	Provider     Provider
	Model        Model
}

// Name is the "provider/model" spelling used by sessions, flags, and errors.
func (s Selection) Name() string {
	return s.ProviderName + "/" + s.Model.ID
}

// Selections returns every declared model, ordered by provider name and then by
// declaration order. Sorting matters: the order a model list is printed in is
// the order a user reads it, and a map iteration would shuffle it.
func (c *Config) Selections() []Selection {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	var selections []Selection
	for _, name := range names {
		provider := c.Providers[name]
		for _, model := range provider.Models {
			selections = append(selections, Selection{
				ProviderName: name,
				Provider:     provider,
				Model:        model,
			})
		}
	}
	return selections
}

// Resolve turns a model name into a provider and a model.
//
// An empty name falls back to DefaultModel, and then to the single declared
// model when the file declares exactly one. The order matters: a config with
// one model needs no defaultModel line, which is the common case for a first
// run.
func (c *Config) Resolve(name string) (Selection, error) {
	if c == nil {
		return Selection{}, errors.New("no config")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(c.DefaultModel)
	}
	if name == "" {
		return c.single()
	}
	// Split only when the prefix is a declared provider, so model ids that
	// contain a slash ("meta-llama/Llama-3") stay addressable by bare id.
	if providerName, modelID, ok := strings.Cut(name, "/"); ok {
		if _, declared := c.Providers[providerName]; declared {
			return c.Lookup(providerName, modelID)
		}
		// No provider answers to that prefix. Report it as a provider problem
		// unless a model claims the whole string, which is what a gateway id
		// with a slash in it looks like.
		if len(c.matchID(name)) == 0 {
			return Selection{}, fmt.Errorf("no provider %q; %s", providerName, c.declaredProviders())
		}
	}
	return c.byID(name)
}

// ResolveSubagent selects a model for the role, falling back to the subagent
// default. Configured reports whether an explicit selection must be used.
func (c *Config) ResolveSubagent(role string) (selection Selection, thinking string, configured bool, err error) {
	if c == nil || c.Subagents == nil {
		return Selection{}, "", false, nil
	}
	model, setting := c.Subagents.Roles[role], "roles."+role
	if model.Model == "" {
		model, setting = c.Subagents.Default, "default"
	}
	if model.Model == "" {
		return Selection{}, "", false, nil
	}
	selection, err = c.Resolve(model.Model)
	if err != nil {
		return Selection{}, "", true, fmt.Errorf("subagents.%s.model: %w", setting, err)
	}
	return selection, model.ThinkingLevel, true, nil
}

// Lookup finds one model by its provider and id. Both names must be exact: a
// session replay depends on resolving the pair it recorded.
func (c *Config) Lookup(providerName, modelID string) (Selection, error) {
	if c == nil {
		return Selection{}, errors.New("no config")
	}
	provider, declared := c.Providers[providerName]
	if !declared {
		return Selection{}, fmt.Errorf("no provider %q; %s", providerName, c.declaredProviders())
	}
	for _, model := range provider.Models {
		if model.ID == modelID {
			return Selection{ProviderName: providerName, Provider: provider, Model: model}, nil
		}
	}
	return Selection{}, fmt.Errorf("provider %q has no model %q; declared: %s", providerName, modelID, strings.Join(modelsOf(provider), ", "))
}

// byID finds a model from a bare id, which only works when the id is unique
// across providers. Ambiguity is an error rather than a first match: picking
// one silently would send the request to whichever provider happened to sort
// first.
func (c *Config) byID(id string) (Selection, error) {
	matches := c.matchID(id)
	switch len(matches) {
	case 0:
		return Selection{}, fmt.Errorf("no model %q; declare it as \"provider/model\" or set \"defaultModel\"", id)
	case 1:
		return matches[0], nil
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match.ProviderName)
	}
	return Selection{}, fmt.Errorf("model %q is declared by %d providers (%s); use \"provider/model\"", id, len(matches), strings.Join(names, ", "))
}

// matchID collects every selection declaring id.
func (c *Config) matchID(id string) []Selection {
	var matches []Selection
	for _, selection := range c.Selections() {
		if selection.Model.ID == id {
			matches = append(matches, selection)
		}
	}
	return matches
}

// single resolves the fallback used when no model was asked for.
func (c *Config) single() (Selection, error) {
	selections := c.Selections()
	switch len(selections) {
	case 0:
		return Selection{}, errors.New("defaultModel is empty and no models are declared")
	case 1:
		return selections[0], nil
	}
	names := make([]string, 0, len(selections))
	for _, selection := range selections {
		names = append(names, selection.Name())
	}
	return Selection{}, fmt.Errorf("defaultModel is empty and %d models are declared; set \"defaultModel\" to one of: %s", len(selections), strings.Join(names, ", "))
}

func (c *Config) providerNames() []string {
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// declaredProviders phrases the provider list for an error, including the
// nothing-declared case, which would otherwise read "declared: ".
func (c *Config) declaredProviders() string {
	names := c.providerNames()
	if len(names) == 0 {
		return "no providers are declared"
	}
	return "declared: " + strings.Join(names, ", ")
}

func modelsOf(provider Provider) []string {
	ids := make([]string, 0, len(provider.Models))
	for _, model := range provider.Models {
		ids = append(ids, model.ID)
	}
	return ids
}

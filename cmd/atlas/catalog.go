package main

import (
	"fmt"
	"slices"
	"strings"

	"pi-go/internal/app"
	"pi-go/internal/catalog"
	"pi-go/internal/config"
	"pi-go/internal/wire"
)

// modelFacts is what a model can do once the config declaration and the
// embedded table are merged. Every surface that reports or acts on a model
// reads it from here, so the printed list, the menu and the request cannot
// disagree about the same model.
type modelFacts struct {
	name          string
	contextWindow int
	maxTokens     int
	input         []string
	gears         []string
}

// factsOf merges a declaration with the table.
//
// The rule is the same for every field: a declared value is a statement about
// this endpoint and wins, and a zero is not a statement, so the table fills it.
// The lookup uses the model id alone because a config's provider name is the
// user's label for an endpoint — "openai" pointing at a proxy still serves
// gpt-5 — and matching it would deny the table to anyone who renamed the
// provider.
//
// Reasoning is the one field the table never fills, because false is not
// "unset": the flag is what makes an adapter send thinking fields at all, and a
// table entry that turned it on for a gateway merely reusing an id would build
// requests that endpoint rejects. The table still narrows the gears of a model
// the config already declares as reasoning, and a declared ThinkingLevels wins
// over both: it is the only place that can say "this endpoint collapses those
// two gears".
func factsOf(selection config.Selection) modelFacts {
	declared := selection.Model
	facts := modelFacts{
		name:          declared.Name,
		contextWindow: declared.ContextWindow,
		maxTokens:     declared.MaxTokens,
		input:         declared.Input,
		gears:         declared.Gears(),
	}
	entry, known := catalog.Default().Lookup(declared.ID)
	if !known {
		if facts.name == "" {
			facts.name = declared.ID
		}
		return facts
	}
	if facts.name == "" {
		facts.name = entry.Name
	}
	if facts.contextWindow == 0 {
		facts.contextWindow = entry.ContextWindow
	}
	if facts.maxTokens == 0 {
		facts.maxTokens = entry.MaxTokens
	}
	if len(facts.input) == 0 {
		facts.input = entry.Input
	}
	if declared.Reasoning && len(declared.ThinkingLevels) == 0 && entry.Reasoning {
		facts.gears = levelNames(entry.Gears())
	}
	// An entry with no display name is possible in the data, and a status line
	// that printed nothing would read as a bug in the status line.
	if facts.name == "" {
		facts.name = declared.ID
	}
	return facts
}

// fitThinking rounds a requested level to one the model offers.
//
// The vocabulary check comes first and stays an error: a typo is not a
// preference, and rounding "hgih" to off would turn a misspelling into a
// silently different request. A real level the model does not offer is a
// preference the table can round, and the adjustment travels back so the caller
// can say it rather than change what was asked for in silence.
//
// A model with no axis at all stays an error too: nothing about that request can
// be sent, and rounding to off would report success for a model that cannot
// think.
func fitThinking(selection config.Selection, level string) (fitted, adjustment string, err error) {
	if level == "" {
		return "", "", nil
	}
	if !config.ValidThinkingLevel(level) {
		return "", "", fmt.Errorf("thinking level %q is not one of: %s", level, strings.Join(config.ThinkingLevels, ", "))
	}
	if level == config.LevelOff {
		// Off is the switch rather than a gear: every model accepts it,
		// including one that cannot think, so it is never fitted.
		return level, "", nil
	}
	gears := factsOf(selection).gears
	if slices.Contains(gears, level) {
		return level, "", nil
	}
	if len(gears) == 0 {
		return "", "", fmt.Errorf("thinking level %q: %s does not think, so only %s can be sent", level, selection.Name(), config.LevelOff)
	}
	fitted = string(catalog.Clamp(levelValues(gears), catalog.Level(level)))
	return fitted, fmt.Sprintf("%s does not offer thinking level %q; using %q (offers: %s)",
		selection.Name(), level, fitted, strings.Join(gears, ", ")), nil
}

// levelValues reads the config vocabulary as catalog gears. The two spell the
// same seven names; the conversion is the seam, not a translation.
func levelValues(gears []string) []catalog.Level {
	values := make([]catalog.Level, len(gears))
	for i, gear := range gears {
		values[i] = catalog.Level(gear)
	}
	return values
}

// levelNames is the other direction: catalog gears as the vocabulary the menu,
// the checks and the adapters already speak.
func levelNames(values []catalog.Level) []string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = string(value)
	}
	return names
}

// modelTable is the switch list the kernel cycles through and answers
// get_available_models with.
//
// It carries each model's thinking levels because the kernel cannot look them
// up: they come from the declaration, the embedded table, and the gears a vendor
// actually exposes, and a transport that guessed at them would be the second
// place deciding what a model can do. The order is the config's, so a cycle walks
// the providers in a stable order rather than a map's.
func modelTable(cfg *config.Config) []app.ModelOption {
	selections := cfg.Selections()
	options := make([]app.ModelOption, 0, len(selections))
	for _, selection := range selections {
		options = append(options, app.ModelOption{
			Model:  modelOf(selection),
			Levels: factsOf(selection).gears,
		})
	}
	return options
}

// thinkingFitter gives the kernel the same rounding the command line uses, so a
// level set over RPC lands where -thinking would have landed it.
//
// The config is the table: a model it no longer declares has nothing to fit
// against, and inventing an axis for it would report a limit that was never read.
// Passing the level through is what the kernel does with no hook at all, which
// keeps "not declared" and "declared without limits" from looking the same.
func thinkingFitter(cfg *config.Config) app.ThinkingFitter {
	return func(model wire.Model, level string) (string, string, error) {
		selection, err := cfg.Lookup(model.Provider, model.ID)
		if err != nil {
			return level, "", nil
		}
		return fitThinking(selection, level)
	}
}

package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/tui"
)

// What is left of the build layer is its two presentations: the printed table
// and the menu rows. The model descriptor and the adapter behind it live in
// internal/assembly, because a window runs the same request and two builders
// would drift — a resumed session would then send a different request than the
// one it recorded.

// writeModelList prints every declared model. The default is marked because
// "which model will atlas use" is the question a list answers.
func writeModelList(out io.Writer, cfg *config.Config) error {
	defaultName := ""
	if selection, err := cfg.Resolve(""); err == nil {
		defaultName = selection.Name()
	}

	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "MODEL\tAPI\tCONTEXT\tMAX TOKENS\tENDPOINT"); err != nil {
		return err
	}
	for _, selection := range cfg.Selections() {
		facts := assembly.FactsOf(selection)
		name := facts.Name
		if selection.Name() == defaultName {
			name += " (default)"
		}
		endpoint := assembly.EffectiveBaseURL(selection)
		if endpoint == "" {
			endpoint = "(adapter default)"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			name, selection.Provider.API, countOrDash(facts.ContextWindow), countOrDash(facts.MaxTokens), endpoint); err != nil {
			return err
		}
	}
	return writer.Flush()
}

// modelChoices is writeModelList as rows. It lives beside the table on purpose:
// "which models exist" has to have one answer, and a menu that offered a model
// the printed list did not would be that answer twice.
//
// Each row also carries the thinking levels that model declares, because that
// set is per model: a menu that offered the same seven positions on every row
// would let a reader pick one the request cannot be made in. level is the one in
// effect, which decides where each row's axis opens.
func modelChoices(cfg *config.Config, level string) []tui.Choice {
	defaultName := ""
	if selection, err := cfg.Resolve(""); err == nil {
		defaultName = selection.Name()
	}
	selections := cfg.Selections()
	out := make([]tui.Choice, 0, len(selections))
	for _, selection := range selections {
		facts := assembly.FactsOf(selection)
		label := facts.Name
		if selection.Name() == defaultName {
			label += " (default)"
		}
		note := selection.Provider.API
		if facts.ContextWindow > 0 {
			// The budget is the one column of the printed table that changes a
			// decision: two models that look alike otherwise differ in how much
			// context they take.
			note += fmt.Sprintf(" · ctx %d", facts.ContextWindow)
		}
		out = append(out, tui.Choice{
			Value: selection.Name(),
			Label: label,
			Note:  note,
			Steps: facts.Gears,
			Step:  gearIndex(facts.Gears, level),
		})
	}
	return out
}

// countOrDash prints "declared: nothing" as a dash rather than a zero, which
// would read as an actual budget.
func countOrDash(value int) string {
	if value == 0 {
		return "-"
	}
	return fmt.Sprint(value)
}

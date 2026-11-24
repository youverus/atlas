package main

import (
	"fmt"
	"io"
	"strings"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

const effortsTrigger = "/efforts"

// effortsCommand adjusts the thinking effort level of models configured with
// thinking support, and prints a clear hint when the current model has no
// thinking effort configured.
func effortsCommand(application *app.App, args []string, pick chooser, out io.Writer) error {
	if application == nil {
		return fmt.Errorf("%s: no active session", effortsTrigger)
	}
	model := application.Model()
	levels, supported := modelEfforts(application)
	if !supported {
		if len(args) > 0 {
			return fmt.Errorf("%s: %s has no thinking effort configured", effortsTrigger, modelField(model))
		}
		fmt.Fprintf(out, "efforts: %s does not support adjusting thinking effort (none configured)\n", modelField(model))
		return nil
	}

	if len(args) == 0 {
		return pickEffort(application, model, levels, pick, out)
	}
	if len(args) > 1 {
		return fmt.Errorf("%s takes one level: %s", effortsTrigger, strings.Join(levels, ", "))
	}
	arg := args[0]
	if strings.EqualFold(arg, "status") {
		return writeEffortStatus(out, model, application.ThinkingLevel(), levels)
	}

	fitted, adjustment, err := application.FitThinking(arg)
	if err != nil {
		return err
	}
	if err := application.SetThinkingLevel(fitted); err != nil {
		return err
	}
	fmt.Fprintf(out, "efforts: %s (%s)\n", modelField(model), fitted)
	if adjustment != "" {
		fmt.Fprintln(out, "warning:", adjustment)
	}
	return nil
}

func pickEffort(application *app.App, model wire.Model, levels []string, pick chooser, out io.Writer) error {
	current := application.ThinkingLevel()
	if pick != nil {
		answer := pick(tui.PickRequest{
			Title:   "efforts · " + modelField(model),
			Items:   effortChoices(levels),
			Current: current,
		})
		if !answer.Taken {
			return nil
		}
		if err := application.SetThinkingLevel(answer.Value); err != nil {
			return err
		}
		fmt.Fprintf(out, "efforts: %s (%s)\n", modelField(model), answer.Value)
		return nil
	}
	return writeEffortStatus(out, model, current, levels)
}

func modelEfforts(application *app.App) ([]string, bool) {
	if application == nil {
		return nil, false
	}
	levels, known := application.AvailableThinkingLevels()
	if !known {
		if !application.Model().Reasoning {
			return nil, false
		}
		levels = append([]string(nil), config.ThinkingLevels...)
	}
	if len(levels) == 0 {
		return nil, false
	}
	if len(levels) == 1 && levels[0] == config.LevelOff {
		return nil, false
	}
	return levels, true
}

func effortChoices(levels []string) []tui.Choice {
	out := make([]tui.Choice, 0, len(levels))
	for _, level := range levels {
		out = append(out, tui.Choice{
			Value: level,
			Label: level,
			Note:  effortNote(level),
		})
	}
	return out
}

func effortNote(level string) string {
	switch level {
	case config.LevelOff:
		return "disable thinking"
	case "medium":
		return "balanced reasoning effort"
	default:
		return "thinking effort"
	}
}

func writeEffortStatus(out io.Writer, model wire.Model, current string, levels []string) error {
	fmt.Fprintf(out, "efforts: %s (%s)\n", modelField(model), current)
	for _, level := range levels {
		mark := " "
		if level == current {
			mark = "*"
		}
		fmt.Fprintf(out, "  %s %s\n", mark, level)
	}
	fmt.Fprintf(out, "\nswitch with /efforts <level>\n")
	return nil
}

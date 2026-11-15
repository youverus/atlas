package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/tui"
)

// apply switches to a model and reports what is in effect.
//
// The level travels with the model, not as a second command: /model fills one
// line, and two lines would be one chance to apply the model and forget the
// level.
func (m models) apply(application *app.App, spec string, out io.Writer) error {
	switched, level, adjustment, err := m.switchModel(spec)
	if err != nil {
		return err
	}
	if level != "" {
		if err := application.SetThinkingLevel(level); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "model: %s%s\n", modelField(switched), thinkingNote(switched, level))
	if adjustment != "" {
		// The line above reports what is in effect; this one says why it is not
		// what was typed, so a clamped :level is a visible decision rather than
		// a typo the session quietly corrected.
		fmt.Fprintln(out, "warning:", adjustment)
	}
	return nil
}

// pick answers a bare /model: a menu when the interface can draw one, the
// printed table when it cannot. Dismissing the menu changes nothing and says
// nothing — the reader asked to look, and looking is not a switch.
func (m models) pick(application *app.App, choose chooser, out io.Writer) error {
	current := currentModel(application)
	if choose != nil && m.choices != nil {
		if items := m.choices(); len(items) > 0 {
			answer := choose(tui.PickRequest{Title: "model", Items: items, Current: current, Axis: thinkingAxis})
			if !answer.Taken {
				return nil
			}
			return m.apply(application, pickSpec(answer, items), out)
		}
	}
	if m.list != nil {
		if err := m.list(out); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "\ncurrent: %s\n", modelField(application.Model()))
	return nil
}

// gearIndex is where a level sits on one row's axis. A level the row does not
// offer falls back to the nearest lower one: that is what the session would
// actually send after switching to it, so the axis never claims a position the
// request cannot be made in.
func gearIndex(gears []string, level string) int {
	if at := slices.Index(config.ThinkingLevels, level); at >= 0 {
		for position := at; position >= 0; position-- {
			if index := slices.Index(gears, config.ThinkingLevels[position]); index >= 0 {
				return index
			}
		}
	}
	return 0
}

// thinkingAxis is the menu's word for the axis it lets the reader turn. The
// interface does not know what a thinking level is; it only prints this word in
// the footer beside ←/→.
const thinkingAxis = "thinking"

// pickSpec turns an answer back into the line /model would have been given: the
// model alone when its row carries no axis, and the position the axis was left
// at when it does. The level rides along with the model for the same reason
// /model m:high keeps them on one line — two changes are one chance to apply
// only half of what the reader chose.
func pickSpec(answer tui.PickAnswer, items []tui.Choice) string {
	for _, item := range items {
		if item.Value != answer.Value {
			continue
		}
		if answer.Step >= 0 && answer.Step < len(item.Steps) {
			return item.Value + ":" + item.Steps[answer.Step]
		}
		break
	}
	return answer.Value
}

// resolveSpec resolves /model's argument. A trailing ":level" is split off only
// when the whole string does not name a model, so an id that contains a colon
// ("llama3:8b") stays addressable and the level syntax works beside it. pi
// resolves scoped models the same way.
//
// The level comes back fitted rather than checked: a gear the model does not
// offer is rounded to the nearest one and the adjustment is reported, because a
// menu pick and a typed suffix reach the same place and neither should fail on a
// position the axis did not offer.
func resolveSpec(cfg *config.Config, spec string) (selection config.Selection, fitted, adjustment string, err error) {
	selection, err = cfg.Resolve(spec)
	if err == nil {
		return selection, "", "", nil
	}
	base, level, ok := cutThinkingLevel(spec)
	if !ok {
		return config.Selection{}, "", "", err
	}
	selection, baseErr := cfg.Resolve(base)
	if baseErr != nil {
		// The base is the model the user meant; its error names what does not
		// exist instead of quoting the suffix back at them.
		return config.Selection{}, "", "", baseErr
	}
	fitted, adjustment, err = fitThinking(selection, level)
	if err != nil {
		return config.Selection{}, "", "", err
	}
	return selection, fitted, adjustment, nil
}

// cutThinkingLevel splits a trailing ":level" off a model spec. The suffix is
// not checked against the vocabulary here: resolveSpec looks the model up
// first, so a bad level is reported as a bad level and not as a missing model.
func cutThinkingLevel(spec string) (base, level string, ok bool) {
	at := strings.LastIndex(spec, ":")
	if at <= 0 || at == len(spec)-1 {
		return "", "", false
	}
	return spec[:at], spec[at+1:], true
}

// sendsThinking reports whether a request at this level carries a thinking
// setting at all. Off is the switch rather than a level, and a model without
// reasoning ignores whatever is set, so neither is an effort worth reporting.
func sendsThinking(reasoning bool, level string) bool {
	return reasoning && level != "" && level != config.LevelOff
}

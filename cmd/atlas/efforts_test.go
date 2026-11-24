package main

import (
	"context"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

func effortApp(t *testing.T, cfg *config.Config, providerName, modelID, thinking string) *app.App {
	t.Helper()
	selection, err := cfg.Lookup(providerName, modelID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	resolver := func(name, id string) (wire.Model, provider.Provider, error) {
		selection, err := cfg.Lookup(name, id)
		if err != nil {
			return wire.Model{}, nil, err
		}
		return assembly.ModelOf(selection), cancelled{}, nil
	}
	application, err := app.New(app.Config{
		Model:         assembly.ModelOf(selection),
		Provider:      cancelled{},
		ThinkingLevel: thinking,
		Models:        assembly.ModelTable(cfg),
		FitThinking:   assembly.ThinkingFitter(cfg),
		Resolver:      resolver,
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application
}

func TestEffortsCommandPromptsWhenModelHasNoThinkingConfig(t *testing.T) {
	cfg := pickerConfig(t)
	application := effortApp(t, cfg, "test", "m1", "off")
	out := &strings.Builder{}

	if _, err := replCommand(context.Background(), application, nil, "/efforts", models{}, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil); err != nil {
		t.Fatalf("/efforts on non-thinking model returned error: %v", err)
	}
	if !strings.Contains(out.String(), "does not support adjusting thinking effort") {
		t.Fatalf("output = %q, want a hint that thinking effort is not configured", out.String())
	}

	if _, err := replCommand(context.Background(), application, nil, "/efforts high", models{}, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil); err == nil {
		t.Fatal("/efforts high on non-thinking model succeeded, want error")
	}
}

func TestEffortsCommandListsAndSwitchesSupportedModel(t *testing.T) {
	cfg := pickerConfig(t)
	application := effortApp(t, cfg, "test", "r1", "low")

	// Bare command without menu lists offered levels and marks current.
	out := &strings.Builder{}
	if _, err := replCommand(context.Background(), application, nil, "/efforts", models{}, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil); err != nil {
		t.Fatalf("bare /efforts: %v", err)
	}
	if !strings.Contains(out.String(), "* low") || !strings.Contains(out.String(), "max") {
		t.Fatalf("list output = %q, want offered gears with * low marked", out.String())
	}

	// Direct argument switches and clamps unoffered gears with a warning.
	out.Reset()
	if _, err := replCommand(context.Background(), application, nil, "/efforts medium", models{}, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil); err != nil {
		t.Fatalf("/efforts medium: %v", err)
	}
	if got := application.ThinkingLevel(); got != "low" {
		t.Fatalf("level after clamped /efforts medium = %q, want low", got)
	}
	if !strings.Contains(out.String(), "warning:") {
		t.Fatalf("output = %q, want clamping warning", out.String())
	}

	// Valid gear switches cleanly.
	out.Reset()
	if _, err := replCommand(context.Background(), application, nil, "/efforts max", models{}, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil); err != nil {
		t.Fatalf("/efforts max: %v", err)
	}
	if got := application.ThinkingLevel(); got != "max" {
		t.Fatalf("level = %q, want max", got)
	}

	// Invalid effort word returns an error.
	if _, err := replCommand(context.Background(), application, nil, "/efforts bogus", models{}, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil); err == nil {
		t.Fatal("/efforts bogus succeeded, want error")
	}
}

func TestEffortsCommandOpensMenuAndRespectsCancel(t *testing.T) {
	cfg := pickerConfig(t)
	application := effortApp(t, cfg, "test", "r1", "high")
	out := &strings.Builder{}

	var asked tui.PickRequest
	cancelPick := func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		return tui.PickAnswer{Taken: false}
	}
	if _, err := replCommand(context.Background(), application, nil, "/efforts", models{}, nil, cancelPick, emptyPrompts(), out, nil, mcpState{}, nil); err != nil {
		t.Fatalf("cancelled /efforts: %v", err)
	}
	if asked.Current != "high" || len(asked.Items) != 4 {
		t.Fatalf("asked = %+v, want Current=high and 4 offered gears (off/low/high/max)", asked)
	}
	if got := application.ThinkingLevel(); got != "high" {
		t.Fatalf("level changed on cancel: %q", got)
	}

	takePick := func(req tui.PickRequest) tui.PickAnswer {
		return tui.PickAnswer{Value: "off", Taken: true}
	}
	if _, err := replCommand(context.Background(), application, nil, "/efforts", models{}, nil, takePick, emptyPrompts(), out, nil, mcpState{}, nil); err != nil {
		t.Fatalf("taken /efforts: %v", err)
	}
	if got := application.ThinkingLevel(); got != "off" {
		t.Fatalf("level = %q, want off", got)
	}
}

func TestEffortsBlockedInSideAndArchivedModes(t *testing.T) {
	if sideCommands[effortsTrigger] {
		t.Fatal("/efforts must not run in side conversation")
	}
	if archivedCommands[effortsTrigger] {
		t.Fatal("/efforts must not run on an archived session")
	}
}

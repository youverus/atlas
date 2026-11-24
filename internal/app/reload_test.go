package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

func TestReloadConfigUpdatesCatalogAndOverridesActiveModelWhenIdle(t *testing.T) {
	manager := newSession(t)
	oldProv := faux.New(faux.Response{Text: "old reply"})
	newProv := faux.New(faux.Response{Text: "new reply"})
	application := newTestApp(t, manager, oldProv, func(cfg *Config) {
		cfg.Models = []ModelOption{
			{Model: wire.Model{ID: "m1", Name: "Model 1", API: "faux", Provider: "p1", ContextWindow: 8000}},
		}
		cfg.Resolver = serveAll(oldProv)
	})

	newModel := wire.Model{ID: "m2", Name: "Model 2", API: "faux", Provider: "p2", ContextWindow: 32000}
	err := application.ReloadConfig(RuntimeConfig{
		Model:         newModel,
		Provider:      newProv,
		Resolver:      serveAll(newProv),
		Models:        []ModelOption{{Model: newModel, Levels: []string{"off", "high"}}, {Model: testModel()}},
		FitThinking:   clampToTable([]ModelOption{{Model: newModel, Levels: []string{"off", "high"}}}),
		OverrideModel: true,
	})
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if got := application.Model(); got.Provider != "p2" || got.ID != "m2" || got.ContextWindow != 32000 {
		t.Fatalf("active model = %+v, want p2/m2", got)
	}
	if _, pending := application.PendingModel(); pending {
		t.Fatal("pending model set after idle reload")
	}
	if len(application.Models()) != 2 {
		t.Fatalf("models len = %d, want 2", len(application.Models()))
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(newProv.Calls()) != 1 || newProv.Calls()[0].Model.ID != "m2" {
		t.Fatalf("newProv calls = %+v, want 1 request to m2", newProv.Calls())
	}
	if len(oldProv.Calls()) != 0 {
		t.Fatalf("oldProv saw %d calls, want 0", len(oldProv.Calls()))
	}
}

func TestReloadConfigPreservesManualModelWhenDefaultUnchangedAndRefreshesMetadata(t *testing.T) {
	manager := newSession(t)
	prov1 := faux.New()
	prov2 := faux.New(faux.Response{Text: "from p2"})
	m1 := wire.Model{ID: "m1", Name: "Old Name", API: "faux", Provider: "p1", ContextWindow: 8000}
	m2 := wire.Model{ID: "m2", Name: "Manual Model", API: "faux", Provider: "p1", ContextWindow: 16000}
	application := newTestApp(t, manager, prov1, func(cfg *Config) {
		cfg.Model = m1
		cfg.Resolver = serveAll(prov1)
		cfg.Models = []ModelOption{{Model: m1}, {Model: m2}}
	})
	if _, err := application.SetModel("p1", "m2"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	m2Updated := wire.Model{ID: "m2", Name: "Updated Manual", API: "faux", Provider: "p1", ContextWindow: 64000}
	resolver := func(p, id string) (wire.Model, provider.Provider, error) {
		if p == "p1" && id == "m2" {
			return m2Updated, prov2, nil
		}
		return m1, prov1, nil
	}
	err := application.ReloadConfig(RuntimeConfig{
		Model:         m1,
		Provider:      prov1,
		Resolver:      resolver,
		Models:        []ModelOption{{Model: m1}, {Model: m2Updated}},
		OverrideModel: false,
	})
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	got := application.Model()
	if got.Provider != "p1" || got.ID != "m2" || got.ContextWindow != 64000 || got.Name != "Updated Manual" {
		t.Fatalf("model = %+v, want updated p1/m2", got)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(prov2.Calls()) != 1 || prov2.Calls()[0].Model.ContextWindow != 64000 {
		t.Fatalf("prov2 calls = %+v, want updated m2 adapter and metadata", prov2.Calls())
	}
}

func TestReloadConfigWhileBusyDefersOverrideUntilNextUserMessage(t *testing.T) {
	manager := newSession(t)
	block := newBlockingTool()
	oldProv := faux.New(
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("c1", "block", json.RawMessage(`{}`))}},
		faux.Response{Text: "finished on old model"},
	)
	newProv := faux.New(faux.Response{Text: "second turn on new model"})
	oldModel := wire.Model{ID: "old-m", Name: "Old", API: "faux", Provider: "p1", ContextWindow: 8000}
	newModel := wire.Model{ID: "new-m", Name: "New", API: "faux", Provider: "p2", ContextWindow: 32000}

	application := newTestApp(t, manager, oldProv, func(cfg *Config) {
		cfg.Model = oldModel
		cfg.Models = []ModelOption{{Model: oldModel}}
		cfg.Tools = StaticTools([]tool.Tool{block})
		cfg.Resolver = func(p, id string) (wire.Model, provider.Provider, error) {
			if p == "p2" && id == "new-m" {
				return newModel, newProv, nil
			}
			return oldModel, oldProv, nil
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("first message"), nil)
		done <- err
	}()

	<-block.started
	err := application.ReloadConfig(RuntimeConfig{
		Model:    newModel,
		Provider: newProv,
		Resolver: func(p, id string) (wire.Model, provider.Provider, error) {
			if p == "p2" && id == "new-m" {
				return newModel, newProv, nil
			}
			return oldModel, oldProv, nil
		},
		Models:        []ModelOption{{Model: oldModel}, {Model: newModel}},
		OverrideModel: true,
	})
	if err != nil {
		t.Fatalf("ReloadConfig while busy: %v", err)
	}
	if got := application.Model(); got.Provider != "p1" || got.ID != "old-m" {
		t.Fatalf("active model changed mid-turn to %+v, want old-m kept", got)
	}
	pending, ok := application.PendingModel()
	if !ok || pending.Provider != "p2" || pending.ID != "new-m" {
		t.Fatalf("pending = %+v (ok=%v), want p2/new-m", pending, ok)
	}
	if len(application.Models()) != 2 {
		t.Fatalf("models = %d, want updated catalog of 2 while busy", len(application.Models()))
	}

	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if len(oldProv.Calls()) != 2 {
		t.Fatalf("oldProv saw %d calls for running turn, want 2", len(oldProv.Calls()))
	}
	if len(newProv.Calls()) != 0 {
		t.Fatalf("newProv saw %d calls before next user message, want 0", len(newProv.Calls()))
	}
	// Still stays on old-m until the next user message arrives.
	if got := application.Model(); got.ID != "old-m" {
		t.Fatalf("model right after turn 1 = %q, want old-m until next user message", got.ID)
	}

	// Next user message activates the reloaded defaultModel.
	res, err := application.Ask(context.Background(), wire.UserMessage("second message"), nil)
	if err != nil {
		t.Fatalf("second ask: %v", err)
	}
	if res.Text() != "second turn on new model" {
		t.Fatalf("second reply = %q", res.Text())
	}
	if got := application.Model(); got.Provider != "p2" || got.ID != "new-m" {
		t.Fatalf("active model on second turn = %+v, want p2/new-m", got)
	}
	if _, ok := application.PendingModel(); ok {
		t.Fatal("pending model remained after second user message applied it")
	}
	if len(newProv.Calls()) != 1 || newProv.Calls()[0].Model.ID != "new-m" {
		t.Fatalf("newProv calls = %+v, want 1 call on new-m", newProv.Calls())
	}
}

func TestSetModelClearsQueuedPendingModelAfterTurnFinishes(t *testing.T) {
	manager := newSession(t)
	gate := newGateProvider()
	prov2 := faux.New()
	prov3 := faux.New(faux.Response{Text: "from manual"})
	m1 := wire.Model{ID: "m1", API: "faux", Provider: "p"}
	m2 := wire.Model{ID: "m2", API: "faux", Provider: "p"}
	m3 := wire.Model{ID: "m3", API: "faux", Provider: "p"}
	resolver := func(p, id string) (wire.Model, provider.Provider, error) {
		switch id {
		case "m2":
			return m2, prov2, nil
		case "m3":
			return m3, prov3, nil
		default:
			return m1, gate, nil
		}
	}
	application := newTestApp(t, manager, gate, func(cfg *Config) {
		cfg.Model = m1
		cfg.Resolver = resolver
	})

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("first"), nil)
		done <- err
	}()
	<-gate.started
	if err := application.ReloadConfig(RuntimeConfig{
		Model:         m2,
		Provider:      prov2,
		Resolver:      resolver,
		OverrideModel: true,
	}); err != nil {
		t.Fatalf("reload while busy: %v", err)
	}
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatalf("first ask: %v", err)
	}

	if _, err := application.SetModel("p", "m3"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if _, ok := application.PendingModel(); ok {
		t.Fatal("SetModel left pendingModel queued")
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("next"), nil); err != nil {
		t.Fatalf("next ask: %v", err)
	}
	if len(prov3.Calls()) != 1 || len(prov2.Calls()) != 0 {
		t.Fatalf("calls prov3=%d prov2=%d, want manual choice m3 to win", len(prov3.Calls()), len(prov2.Calls()))
	}
}

func TestReloadConfigWhileViewingArchivedLeavesArchivedFileUntouched(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "archived answer"}), func(cfg *Config) {
		cfg.Resolver = serveAll(faux.New())
	})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("seed ask: %v", err)
	}
	file := application.Session().File()
	if err := application.Archive(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := application.SwitchSession(file); err != nil {
		t.Fatalf("open archived: %v", err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read archived: %v", err)
	}

	nextProv := faux.New(faux.Response{Text: "fresh"})
	newModel := wire.Model{ID: "new-default", API: "faux", Provider: "p2"}
	if err := application.ReloadConfig(RuntimeConfig{
		Model:         newModel,
		Provider:      nextProv,
		Models:        []ModelOption{{Model: newModel}},
		OverrideModel: true,
	}); err != nil {
		t.Fatalf("ReloadConfig on archived view: %v", err)
	}

	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read archived after reload: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("ReloadConfig modified the archived session file")
	}
	if err := application.NewSessionLeavingArchived(); err != nil {
		t.Fatalf("NewSessionLeavingArchived: %v", err)
	}
	if got := application.Model(); got.Provider != "p2" || got.ID != "new-default" {
		t.Fatalf("new session model = %+v, want p2/new-default", got)
	}
}

func TestReloadConfigWhileBusyRefreshesSameModelProviderForNextTurn(t *testing.T) {
	manager := newSession(t)
	block := newBlockingTool()
	oldProv := faux.New(
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("c1", "block", json.RawMessage(`{}`))}},
		faux.Response{Text: "finished on stale key"},
	)
	newProv := faux.New(faux.Response{Text: "reply with updated key"})
	model := wire.Model{ID: "deepseek-flash", Name: "DeepSeek Flash", API: "faux", Provider: "deepseek", ContextWindow: 64000}

	application := newTestApp(t, manager, oldProv, func(cfg *Config) {
		cfg.Model = model
		cfg.Models = []ModelOption{{Model: model}}
		cfg.Tools = StaticTools([]tool.Tool{block})
		cfg.Resolver = serveAll(oldProv)
	})

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("first"), nil)
		done <- err
	}()
	<-block.started

	if err := application.ReloadConfig(RuntimeConfig{
		Model:         model,
		Provider:      newProv,
		Resolver:      serveAll(newProv),
		Models:        []ModelOption{{Model: model}},
		OverrideModel: false,
	}); err != nil {
		t.Fatalf("ReloadConfig while busy: %v", err)
	}

	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if len(oldProv.Calls()) != 2 {
		t.Fatalf("oldProv calls = %d, want 2", len(oldProv.Calls()))
	}

	res, err := application.Ask(context.Background(), wire.UserMessage("second"), nil)
	if err != nil {
		t.Fatalf("second ask: %v", err)
	}
	if res.Text() != "reply with updated key" {
		t.Fatalf("second reply = %q, want reply with updated key", res.Text())
	}
	if len(newProv.Calls()) != 1 {
		t.Fatalf("newProv calls = %d, want 1", len(newProv.Calls()))
	}
}

func TestReloadConfigUpdatesOpenSideSession(t *testing.T) {
	manager := newSession(t)
	block := newBlockingTool()
	oldProv := faux.New(
		faux.Response{Text: "side old"},
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("c1", "block", json.RawMessage(`{}`))}},
		faux.Response{Text: "side busy finish"},
	)
	newProv := faux.New(
		faux.Response{Text: "side after idle reload"},
		faux.Response{Text: "side after busy reload"},
	)
	model := wire.Model{ID: "deepseek-flash", Name: "DeepSeek Flash", API: "faux", Provider: "deepseek", ContextWindow: 64000}

	application := newTestApp(t, manager, oldProv, func(cfg *Config) {
		cfg.Model = model
		cfg.Models = []ModelOption{{Model: model}}
		cfg.Tools = StaticTools([]tool.Tool{block})
		cfg.Resolver = serveAll(oldProv)
	})

	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	defer application.EndSide()

	res, err := side.Ask(context.Background(), wire.UserMessage("side 1"), nil)
	if err != nil {
		t.Fatalf("side ask 1: %v", err)
	}
	if res.Text() != "side old" {
		t.Fatalf("side 1 = %q", res.Text())
	}

	// Idle side picks up the new provider immediately on reload.
	if err := application.ReloadConfig(RuntimeConfig{
		Model:         model,
		Provider:      newProv,
		Resolver:      serveAll(newProv),
		Models:        []ModelOption{{Model: model}},
		OverrideModel: true,
	}); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	res, err = side.Ask(context.Background(), wire.UserMessage("side 2"), nil)
	if err != nil {
		t.Fatalf("side ask 2: %v", err)
	}
	if res.Text() != "side after idle reload" {
		t.Fatalf("side 2 = %q", res.Text())
	}

	// Also verify reload while side is busy does not race and takes effect after turn completes.
	side2, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide 2: %v", err)
	}
	// Reset side2 back to oldProv by reloading, then run blocking turn.
	if err := application.ReloadConfig(RuntimeConfig{
		Model:         model,
		Provider:      oldProv,
		Resolver:      serveAll(oldProv),
		Models:        []ModelOption{{Model: model}},
		OverrideModel: true,
	}); err != nil {
		t.Fatalf("ReloadConfig back to oldProv: %v", err)
	}

	sideDone := make(chan error, 1)
	go func() {
		_, askErr := side2.Ask(context.Background(), wire.UserMessage("side blocking"), nil)
		sideDone <- askErr
	}()
	<-block.started

	if err := application.ReloadConfig(RuntimeConfig{
		Model:         model,
		Provider:      newProv,
		Resolver:      serveAll(newProv),
		Models:        []ModelOption{{Model: model}},
		OverrideModel: true,
	}); err != nil {
		t.Fatalf("ReloadConfig while side busy: %v", err)
	}
	close(block.release)
	if err := <-sideDone; err != nil {
		t.Fatalf("side blocking ask: %v", err)
	}

	res, err = side2.Ask(context.Background(), wire.UserMessage("side after busy"), nil)
	if err != nil {
		t.Fatalf("side after busy ask: %v", err)
	}
	if res.Text() != "side after busy reload" {
		t.Fatalf("side after busy = %q", res.Text())
	}
}

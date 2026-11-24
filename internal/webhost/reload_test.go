package webhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

func TestHostReloadConfigAndPendingModelReporting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	write(`{
		"defaultModel": "p1/m1",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`)

	kernel, _, err := assembly.New(context.Background(), assembly.Options{
		ConfigPath: cfgPath,
		Workdir:    t.TempDir(),
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	host := NewWith(kernel, Options{Projects: filepath.Join(t.TempDir(), "projects.json")})
	if got := host.SessionModel(); got != "p1/m1" {
		t.Fatalf("SessionModel = %q, want p1/m1", got)
	}

	write(`{
		"defaultModel": "p1/m2",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [
					{"id": "m1", "name": "One", "contextWindow": 8000},
					{"id": "m2", "name": "Two", "contextWindow": 32000}
				]
			}
		}
	}`)
	if err := host.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if got := host.SessionModel(); got != "p1/m2" {
		t.Fatalf("SessionModel after reload = %q, want p1/m2", got)
	}
	list, err := host.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if list.Current != "p1/m2" || list.Pending != "" || len(list.Models) != 2 {
		t.Fatalf("Models = %+v, want current=p1/m2 pending='' len=2", list)
	}
}

type blockingHostTool struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingHostTool) Name() string        { return "block" }
func (b *blockingHostTool) Label() string       { return "Block" }
func (b *blockingHostTool) Description() string { return "Block" }
func (b *blockingHostTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (b *blockingHostTool) Execute(ctx context.Context, _ tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	close(b.started)
	select {
	case <-b.release:
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	return tool.Result{Content: []wire.Content{wire.TextContent("ok")}}, nil
}

func TestHostModelsReportsPendingSwitchWhileTurnRuns(t *testing.T) {
	block := &blockingHostTool{started: make(chan struct{}), release: make(chan struct{})}
	oldProv := faux.New(
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("c1", "block", json.RawMessage(`{}`))}},
		faux.Response{Text: "old finished"},
	)
	newProv := faux.New(faux.Response{Text: "new turn"})
	m1 := wire.Model{ID: "m1", Name: "One", API: "faux", Provider: "p1"}
	m2 := wire.Model{ID: "m2", Name: "Two", API: "faux", Provider: "p2"}

	manager, err := session.Create(t.TempDir(), t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application, err := app.New(app.Config{
		Model:        m1,
		Provider:     oldProv,
		SystemPrompt: app.StaticPrompt("base"),
		Tools:        app.StaticTools([]tool.Tool{block}),
		Models:       []app.ModelOption{{Model: m1}},
		Resolver: func(p, id string) (wire.Model, provider.Provider, error) {
			if p == "p2" {
				return m2, newProv, nil
			}
			return m1, oldProv, nil
		},
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	host := NewWith(&assembly.Kernel{App: application, Manager: manager, Cwd: manager.Cwd()}, Options{
		Projects: filepath.Join(t.TempDir(), "projects.json"),
	})

	done := make(chan error, 1)
	go func() {
		_, askErr := application.Ask(context.Background(), wire.UserMessage("first"), nil)
		done <- askErr
	}()
	<-block.started

	if err := application.ReloadConfig(app.RuntimeConfig{
		Model:         m2,
		Provider:      newProv,
		Models:        []app.ModelOption{{Model: m1}, {Model: m2}},
		OverrideModel: true,
	}); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	list, err := host.Models()
	if err != nil {
		t.Fatalf("Models while busy: %v", err)
	}
	if list.Current != "p1/m1" || list.Pending != "p2/m2" {
		t.Fatalf("Models while busy = %+v, want current=p1/m1 pending=p2/m2", list)
	}

	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("second"), nil); err != nil {
		t.Fatalf("second ask: %v", err)
	}
	after, err := host.Models()
	if err != nil {
		t.Fatalf("Models after second ask: %v", err)
	}
	if after.Current != "p2/m2" || after.Pending != "" {
		t.Fatalf("Models after second ask = %+v, want current=p2/m2 pending=''", after)
	}
}

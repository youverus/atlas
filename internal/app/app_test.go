package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// testModel is the default descriptor for a faux-backed conversation.
func testModel() wire.Model {
	return wire.Model{ID: "faux-1", Name: "Faux One", API: "faux", Provider: "faux"}
}

func newSession(t *testing.T) *session.Manager {
	t.Helper()
	manager, err := session.Create(t.TempDir(), t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return manager
}

// newTestApp builds an app with a base prompt and the given provider. Extra
// functions tweak the config before construction.
func newTestApp(t *testing.T, manager *session.Manager, prov provider.Provider, configure ...func(*Config)) *App {
	t.Helper()
	cfg := Config{
		Model:        testModel(),
		Provider:     prov,
		SystemPrompt: StaticPrompt("base prompt"),
	}
	for _, fn := range configure {
		fn(&cfg)
	}
	application, err := New(cfg, manager)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return application
}

// serveAll resolves every provider/model pair to prov, keeping the requested
// names so a switch is visible in the request.
func serveAll(prov provider.Provider) Resolver {
	return func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		model := testModel()
		model.Provider = providerName
		model.ID = modelID
		return model, prov, nil
	}
}

// gateProvider keeps a turn in flight until release is closed.
type gateProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGateProvider() *gateProvider {
	return &gateProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateProvider) API() string { return "faux" }

func (g *gateProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return faux.New(faux.Response{Text: "done"}).Stream(ctx, req)
}

// blockingTool signals when it starts and waits until released, which is a
// deterministic moment to send a steering message.
type blockingTool struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingTool() *blockingTool {
	return &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingTool) Name() string        { return "block" }
func (b *blockingTool) Label() string       { return "Block" }
func (b *blockingTool) Description() string { return "Block until released." }

func (b *blockingTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (b *blockingTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	close(b.started)
	select {
	case <-b.release:
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	return tool.Result{Content: []wire.Content{wire.TextContent("released")}}, nil
}

func thinkingEntries(t *testing.T, manager *session.Manager) []session.Entry {
	t.Helper()
	var entries []session.Entry
	for _, entry := range manager.Entries() {
		if entry.Type == session.TypeThinkingLevelChange {
			entries = append(entries, entry)
		}
	}
	return entries
}

func modelEntries(t *testing.T, manager *session.Manager) []session.Entry {
	t.Helper()
	var entries []session.Entry
	for _, entry := range manager.Entries() {
		if entry.Type == session.TypeModelChange {
			entries = append(entries, entry)
		}
	}
	return entries
}

func TestNewValidatesConfig(t *testing.T) {
	manager := newSession(t)
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no provider", Config{Model: testModel()}, "Provider"},
		{"no model", Config{Provider: faux.New()}, "Model.ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg, manager); err == nil {
				t.Fatalf("expected an error mentioning %s", tc.want)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %s", err, tc.want)
			}
		})
	}

	if _, err := New(Config{Model: testModel(), Provider: faux.New()}, nil); err == nil {
		t.Fatal("expected an error for a missing session manager")
	}
}

func TestAskPersistsEveryMessage(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "hello there"})
	application := newTestApp(t, manager, prov)

	// A zero role is treated as a user prompt, which is what a caller typing
	// text expects.
	result, err := application.Ask(context.Background(), wire.Message{Content: wire.NewTextList("hi")}, nil)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Text() != "hello there" {
		t.Fatalf("text = %q, want %q", result.Text(), "hello there")
	}
	if result.StopReason != wire.StopStop {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, wire.StopStop)
	}
	if result.Assistant == nil {
		t.Fatal("assistant message is nil")
	}
	if result.Assistant.Model != "faux-1" || result.Assistant.Provider != "faux" {
		t.Fatalf("assistant names %s/%s, want faux/faux-1", result.Assistant.Provider, result.Assistant.Model)
	}

	projected, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(projected.Messages) != 2 {
		t.Fatalf("session has %d messages, want 2", len(projected.Messages))
	}
	if projected.Messages[0].Role != wire.RoleUser || projected.Messages[0].Text() != "hi" {
		t.Fatalf("first message = %+v, want user %q", projected.Messages[0], "hi")
	}
	if projected.Messages[0].Timestamp == 0 {
		t.Fatal("persisted prompt has no timestamp")
	}
	if projected.Messages[1].Role != wire.RoleAssistant {
		t.Fatalf("second message role = %q, want assistant", projected.Messages[1].Role)
	}

	if _, err := os.Stat(manager.File()); err != nil {
		t.Fatalf("session file was not written: %v", err)
	}
}

func TestAskPersistsToolResults(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "echo", json.RawMessage(`{"text":"ping"}`)),
		}},
		faux.Response{Text: "done"},
	)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Tools = StaticTools([]tool.Tool{echoTool{}})
	})

	result, err := application.Ask(context.Background(), wire.UserMessage("go"), nil)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Text() != "done" {
		t.Fatalf("text = %q, want done", result.Text())
	}

	projected, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	var roles []wire.Role
	for _, message := range projected.Messages {
		roles = append(roles, message.Role)
	}
	want := []wire.Role{wire.RoleUser, wire.RoleAssistant, wire.RoleToolResult, wire.RoleAssistant}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v", roles, want)
		}
	}
	if projected.Messages[2].Text() != "echo: ping" {
		t.Fatalf("tool result = %q, want %q", projected.Messages[2].Text(), "echo: ping")
	}
}

// echoTool is a local copy of the echo tool so this package's tests do not
// depend on internal/tool/echo.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Label() string       { return "Echo" }
func (echoTool) Description() string { return "Echo text." }

func (echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}

func (echoTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := tool.Decode(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: []wire.Content{wire.TextContent("echo: " + args.Text)}}, nil
}

func TestAskRejectsConcurrentTurn(t *testing.T) {
	manager := newSession(t)
	prov := newGateProvider()
	application := newTestApp(t, manager, prov)

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("first"), nil)
		done <- err
	}()

	<-prov.started
	if !application.Busy() {
		t.Fatal("app reports idle while a turn is running")
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("second"), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second ask err = %v, want ErrBusy", err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if application.Busy() {
		t.Fatal("app still busy after the turn finished")
	}
}

func TestMutationsRejectedWhileBusy(t *testing.T) {
	manager := newSession(t)
	prov := newGateProvider()
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Resolver = serveAll(faux.New())
	})
	entryID := mustAppend(t, manager, wire.UserMessage("earlier"))

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("running"), nil)
		done <- err
	}()

	<-prov.started
	if _, err := application.SetModel("faux", "other"); !errors.Is(err, ErrBusy) {
		t.Fatalf("SetModel err = %v, want ErrBusy", err)
	}
	if err := application.SetThinkingLevel("high"); !errors.Is(err, ErrBusy) {
		t.Fatalf("SetThinkingLevel err = %v, want ErrBusy", err)
	}
	if err := application.Branch(entryID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Branch err = %v, want ErrBusy", err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}
}

func TestSetModelRecordsChangeAndUsesNewModel(t *testing.T) {
	manager := newSession(t)
	first := faux.New(faux.Response{Text: "first"})
	second := faux.New(faux.Response{Text: "second"})
	application := newTestApp(t, manager, first, func(cfg *Config) {
		cfg.Resolver = serveAll(second)
	})

	model, err := application.SetModel("openai", "gpt-x")
	if err != nil {
		t.Fatalf("set model: %v", err)
	}
	if model.ID != "gpt-x" || model.Provider != "openai" {
		t.Fatalf("model = %s/%s, want openai/gpt-x", model.Provider, model.ID)
	}
	if got := application.Model().ID; got != "gpt-x" {
		t.Fatalf("app model = %q, want gpt-x", got)
	}

	entries := modelEntries(t, manager)
	if len(entries) != 1 {
		t.Fatalf("model_change entries = %d, want 1", len(entries))
	}
	if entries[0].Provider != "openai" || entries[0].ModelID != "gpt-x" {
		t.Fatalf("recorded %s/%s, want openai/gpt-x", entries[0].Provider, entries[0].ModelID)
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	calls := second.Calls()
	if len(calls) != 1 {
		t.Fatalf("new provider saw %d requests, want 1", len(calls))
	}
	if calls[0].Model.ID != "gpt-x" {
		t.Fatalf("request model = %q, want gpt-x", calls[0].Model.ID)
	}
	if len(first.Calls()) != 0 {
		t.Fatalf("old provider saw %d requests, want 0", len(first.Calls()))
	}
}

func TestSetModelKeepsStateOnResolveFailure(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.Resolver = func(providerName, modelID string) (wire.Model, provider.Provider, error) {
			return wire.Model{}, nil, errors.New("unknown model")
		}
	})

	if _, err := application.SetModel("nope", "nope"); err == nil {
		t.Fatal("expected a resolve error")
	}
	if got := application.Model().ID; got != "faux-1" {
		t.Fatalf("model = %q, want the configured faux-1", got)
	}
	if entries := modelEntries(t, manager); len(entries) != 0 {
		t.Fatalf("model_change entries = %d, want 0", len(entries))
	}
}

func TestSetModelNeedsResolver(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	if _, err := application.SetModel("faux", "faux-2"); err == nil {
		t.Fatal("expected an error without a resolver")
	}
}

func TestSetThinkingLevelRecordsOnlyChanges(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New())

	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set high: %v", err)
	}
	if got := application.ThinkingLevel(); got != "high" {
		t.Fatalf("level = %q, want high", got)
	}
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set high again: %v", err)
	}
	if entries := thinkingEntries(t, manager); len(entries) != 1 {
		t.Fatalf("thinking entries = %d, want 1 after an unchanged set", len(entries))
	}

	if err := application.SetThinkingLevel(""); err != nil {
		t.Fatalf("set empty: %v", err)
	}
	if got := application.ThinkingLevel(); got != session.DefaultThinkingLevel {
		t.Fatalf("level = %q, want %q", got, session.DefaultThinkingLevel)
	}
	entries := thinkingEntries(t, manager)
	if len(entries) != 2 {
		t.Fatalf("thinking entries = %d, want 2", len(entries))
	}
	if entries[1].ThinkingLevel != session.DefaultThinkingLevel {
		t.Fatalf("recorded %q, want %q", entries[1].ThinkingLevel, session.DefaultThinkingLevel)
	}
}

func TestThinkingLevelReachesProvider(t *testing.T) {
	prov := faux.New(faux.Response{Text: "ok"})
	application := newTestApp(t, newSession(t), prov)
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set level: %v", err)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	calls := prov.Calls()
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
	if calls[0].Options.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", calls[0].Options.ThinkingLevel)
	}
}

// optionTable is a model list as a front end builds one: a model with an axis,
// and a model that cannot think. The second is what separates "no axis" from
// "not in the list".
func optionTable() []ModelOption {
	return []ModelOption{
		{
			Model:  wire.Model{ID: "m-1", Name: "M One", API: "faux", Provider: "p", Reasoning: true},
			Levels: []string{"off", "low", "high"},
		},
		{Model: wire.Model{ID: "m-2", Name: "M Two", API: "faux", Provider: "p"}},
	}
}

// clampToTable is the hook a front end supplies, reduced to what these tests
// need: a listed level is kept, an unlisted one is rounded to the top of the
// row, and a row with no axis refuses every gear but off.
func clampToTable(table []ModelOption) ThinkingFitter {
	return func(model wire.Model, level string) (string, string, error) {
		for _, option := range table {
			if option.Model.Provider != model.Provider || option.Model.ID != model.ID {
				continue
			}
			if len(option.Levels) == 0 {
				if level == "off" {
					return level, "", nil
				}
				return "", "", errors.New(model.ID + " does not think, so only off can be sent")
			}
			if slices.Contains(option.Levels, level) {
				return level, "", nil
			}
			return option.Levels[len(option.Levels)-1], "rounded to the nearest gear", nil
		}
		return level, "", nil
	}
}

func TestModelsAndThinkingLevelsComeFromTheTable(t *testing.T) {
	table := optionTable()
	prov := faux.New()
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Model = table[0].Model
		cfg.Models = table
		cfg.Resolver = serveAll(prov)
		cfg.FitThinking = clampToTable(table)
	})

	options := application.Models()
	if len(options) != len(table) || options[0].Model.ID != "m-1" {
		t.Fatalf("models = %+v, want the configured list", options)
	}
	// The copy is the contract: a front end renders this list and must not be
	// able to edit what the kernel cycles through.
	options[0].Model.ID = "tampered"
	options[0].Levels[0] = "tampered"
	if again := application.Models(); again[0].Model.ID != "m-1" || again[0].Levels[0] != "off" {
		t.Fatalf("models = %+v, want the app's own copy", again[0])
	}

	levels, known := application.AvailableThinkingLevels()
	if !known || strings.Join(levels, ",") != "off,low,high" {
		t.Fatalf("levels = %q known = %v, want the model's axis", levels, known)
	}

	// A level the hook does not like comes back fitted, with the reason, and
	// nothing is applied: deciding is the caller's job.
	fitted, adjustment, err := application.FitThinking("max")
	if err != nil {
		t.Fatalf("fit max: %v", err)
	}
	if fitted != "high" || adjustment == "" {
		t.Fatalf("fitted = %q adjustment = %q, want the top gear and a reason", fitted, adjustment)
	}
	if application.ThinkingLevel() != "high" {
		t.Fatalf("level = %q, want fitting to change nothing", application.ThinkingLevel())
	}

	// A model the list does not hold is not a model without an axis.
	if _, err := application.SetModel("faux", "faux-1"); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	if levels, known := application.AvailableThinkingLevels(); known || levels != nil {
		t.Fatalf("levels = %q known = %v, want no answer for an unlisted model", levels, known)
	}
}

func TestCycleModelCarriesTheThinkingLevel(t *testing.T) {
	table := optionTable()
	prov := faux.New()
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Model = table[0].Model
		cfg.Models = table
		cfg.Resolver = serveAll(prov)
		cfg.FitThinking = clampToTable(table)
	})
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set high: %v", err)
	}

	cycle, ok, err := application.CycleModel()
	if err != nil || !ok {
		t.Fatalf("cycle: ok = %v err = %v, want a switch", ok, err)
	}
	if cycle.Model.ID != "m-2" {
		t.Fatalf("model = %+v, want the next entry", cycle.Model)
	}
	// m-2 cannot think, so the level follows the model down rather than
	// staying on a gear the next request would drop.
	if cycle.ThinkingLevel != "off" || application.ThinkingLevel() != "off" {
		t.Fatalf("level = %q / %q, want off", cycle.ThinkingLevel, application.ThinkingLevel())
	}

	cycle, ok, err = application.CycleModel()
	if err != nil || !ok {
		t.Fatalf("cycle back: ok = %v err = %v, want a switch", ok, err)
	}
	if cycle.Model.ID != "m-1" {
		t.Fatalf("model = %+v, want the wrap back to the first entry", cycle.Model)
	}

	// One model is not a ring, and saying so is a result rather than a failure.
	short := newTestApp(t, newSession(t), faux.New(), func(cfg *Config) {
		cfg.Model = table[0].Model
		cfg.Models = table[:1]
	})
	if _, ok, err := short.CycleModel(); err != nil || ok {
		t.Fatalf("ok = %v err = %v, want nothing to cycle", ok, err)
	}
}

func TestCycleThinkingLevelWalksTheDeclaredAxis(t *testing.T) {
	table := optionTable()
	prov := faux.New()
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Model = table[0].Model
		cfg.Models = table
		cfg.Resolver = serveAll(prov)
	})

	// Pin the starting level: this test is about the walk, not about where a
	// fresh session begins. Off is the first gear of the declared axis, so the
	// first cycle lands on the next one.
	if err := application.SetThinkingLevel("off"); err != nil {
		t.Fatalf("set off: %v", err)
	}
	level, ok, err := application.CycleThinkingLevel()
	if err != nil || !ok || level != "low" {
		t.Fatalf("level = %q ok = %v err = %v, want low", level, ok, err)
	}
	if application.ThinkingLevel() != "low" {
		t.Fatalf("level = %q, want the cycled one applied", application.ThinkingLevel())
	}

	// A level off the axis starts the walk at its first entry, which is pi's
	// rule and the only answer that does not repeat a level nothing sends.
	if err := application.SetThinkingLevel("max"); err != nil {
		t.Fatalf("set max: %v", err)
	}
	if level, ok, err := application.CycleThinkingLevel(); err != nil || !ok || level != "off" {
		t.Fatalf("level = %q ok = %v err = %v, want the first gear", level, ok, err)
	}

	// A model that cannot think has no axis to walk.
	if _, err := application.SetModel("p", "m-2"); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	if level, ok, err := application.CycleThinkingLevel(); err != nil || ok || level != "" {
		t.Fatalf("level = %q ok = %v err = %v, want nothing to cycle", level, ok, err)
	}
}

func TestNewAdoptsSessionModelAndThinkingLevel(t *testing.T) {
	manager := newSession(t)
	mustAppend(t, manager, wire.UserMessage("earlier"))
	if _, err := manager.AppendModelChange("anthropic", "claude-x"); err != nil {
		t.Fatalf("append model change: %v", err)
	}
	if _, err := manager.AppendThinkingLevelChange("high"); err != nil {
		t.Fatalf("append thinking level: %v", err)
	}

	restored := faux.New(faux.Response{Text: "resumed"})
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.Resolver = serveAll(restored)
	})

	if got := application.Model().ID; got != "claude-x" {
		t.Fatalf("model = %q, want claude-x", got)
	}
	if got := application.ThinkingLevel(); got != "high" {
		t.Fatalf("level = %q, want high", got)
	}
	if note := application.RestoreNote(); note != "" {
		t.Fatalf("restore note = %q, want empty", note)
	}
	if messages := application.Messages(); len(messages) != 1 {
		t.Fatalf("transcript has %d messages, want 1", len(messages))
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("continue"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	calls := restored.Calls()
	if len(calls) != 1 {
		t.Fatalf("restored provider saw %d requests, want 1", len(calls))
	}
	if calls[0].Model.ID != "claude-x" || calls[0].Options.ThinkingLevel != "high" {
		t.Fatalf("request used %s at level %q", calls[0].Model.ID, calls[0].Options.ThinkingLevel)
	}
}

func TestNewKeepsConfigModelWhenResolverFails(t *testing.T) {
	manager := newSession(t)
	mustAppend(t, manager, wire.UserMessage("earlier"))
	if _, err := manager.AppendModelChange("anthropic", "claude-x"); err != nil {
		t.Fatalf("append model change: %v", err)
	}

	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.Resolver = func(providerName, modelID string) (wire.Model, provider.Provider, error) {
			return wire.Model{}, nil, errors.New("not configured")
		}
	})
	if got := application.Model().ID; got != "faux-1" {
		t.Fatalf("model = %q, want the configured faux-1", got)
	}
	if note := application.RestoreNote(); !strings.Contains(note, "claude-x") {
		t.Fatalf("restore note = %q, want it to mention claude-x", note)
	}
}

func TestNewKeepsConfigModelWithoutResolver(t *testing.T) {
	manager := newSession(t)
	mustAppend(t, manager, wire.UserMessage("earlier"))
	if _, err := manager.AppendModelChange("anthropic", "claude-x"); err != nil {
		t.Fatalf("append model change: %v", err)
	}

	application := newTestApp(t, manager, faux.New())
	if got := application.Model().ID; got != "faux-1" {
		t.Fatalf("model = %q, want the configured faux-1", got)
	}
	if note := application.RestoreNote(); !strings.Contains(note, "resolver") {
		t.Fatalf("restore note = %q, want it to mention the resolver", note)
	}
}

func TestNewIgnoresSessionModelWithoutMessages(t *testing.T) {
	manager := newSession(t)
	if _, err := manager.AppendModelChange("anthropic", "claude-x"); err != nil {
		t.Fatalf("append model change: %v", err)
	}

	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.Resolver = serveAll(faux.New())
	})
	if got := application.Model().ID; got != "faux-1" {
		t.Fatalf("model = %q, want the configured faux-1", got)
	}
}

func TestNewAdoptsSessionSystemPrompt(t *testing.T) {
	manager := newSession(t)
	mustAppend(t, manager, wire.SystemMessage("prompt from the session"))
	mustAppend(t, manager, wire.UserMessage("earlier"))

	application := newTestApp(t, manager, faux.New())
	if got := application.SystemPrompt(); got != "prompt from the session" {
		t.Fatalf("prompt = %q, want the session prompt", got)
	}
}

// The prompt is a file the user can rewrite while the session is running (/init
// writes AGENTS.md), so the next request has to carry the new text and the
// transcript has to survive the change.
func TestSetSystemPromptReachesTheNextRequest(t *testing.T) {
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	application := newTestApp(t, newSession(t), prov)
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	if err := application.SetSystemPrompt("prompt with the project rules"); err != nil {
		t.Fatalf("set prompt: %v", err)
	}
	if got := application.SystemPrompt(); got != "prompt with the project rules" {
		t.Fatalf("prompt = %q, want the new one", got)
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the transcript to survive", got)
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	calls := prov.Calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	if len(calls[1].Messages) == 0 || calls[1].Messages[0].Role != wire.RoleSystem {
		t.Fatalf("second request = %+v, want a system message first", calls[1].Messages)
	}
	if got := calls[1].Messages[0].Content.Text(); got != "prompt with the project rules" {
		t.Fatalf("system message = %q, want the new prompt", got)
	}
}

func TestBranchRebuildsTranscript(t *testing.T) {
	manager := newSession(t)
	first := mustAppend(t, manager, wire.UserMessage("one"))
	mustAppend(t, manager, wire.UserMessage("two"))
	application := newTestApp(t, manager, faux.New())
	if messages := application.Messages(); len(messages) != 2 {
		t.Fatalf("transcript has %d messages, want 2", len(messages))
	}

	if err := application.Branch(first); err != nil {
		t.Fatalf("branch: %v", err)
	}
	messages := application.Messages()
	if len(messages) != 1 || messages[0].Text() != "one" {
		t.Fatalf("transcript = %+v, want only %q", messages, "one")
	}
}

func TestBranchRejectsUnknownEntry(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	if err := application.Branch("missing"); err == nil {
		t.Fatal("expected an error for an unknown entry")
	}
}

// autoName runs one exchange and reports the name the session carries.
func autoName(t *testing.T, manager *session.Manager, prompts ...string) *App {
	t.Helper()
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "done"}))
	for _, prompt := range prompts {
		if _, err := application.Ask(context.Background(), wire.UserMessage(prompt), nil); err != nil {
			t.Fatalf("ask %q: %v", prompt, err)
		}
	}
	return application
}

// A conversation names itself from the first thing the reader said, which is
// what both frontends show in their session list.
func TestFirstUserMessageNamesTheSession(t *testing.T) {
	manager := newSession(t)
	application := autoName(t, manager, "用 read 工具读 go.mod，只回答第一行的 module 名称")
	if got, want := application.Name(), "用 read 工具读 go.mod"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

// A name the reader is watching must not change under them, so only the first
// message decides it.
func TestLaterMessagesKeepTheFirstNames(t *testing.T) {
	manager := newSession(t)
	application := autoName(t, manager, "fix the parser", "and then run the tests")
	if got, want := application.Name(), "fix the parser"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

// A name set by hand outranks a derived one, and naming by hand stays possible
// after the first message.
func TestSetNameOutranksTheDerivedName(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "done"}))
	if err := application.SetName("release"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("fix the parser"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got, want := application.Name(), "release"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

// A message with no text names nothing. The conversation stays unnamed until
// something is actually said, rather than being labelled with an empty string.
func TestMessageWithoutTextLeavesTheSessionUnnamed(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "done"}))
	image := wire.UserMessageBlocks(wire.ImageContent("AAAA", "image/png"))
	if _, err := application.Ask(context.Background(), image, nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := application.Name(); got != "" {
		t.Fatalf("name = %q, want it to stay empty", got)
	}
}

// The name lives in the session file as an entry, but it is a label rather than
// something the model said: the context the next request is built from must not
// gain a message from it.
func TestDerivedNameStaysOutOfTheContext(t *testing.T) {
	manager := newSession(t)
	autoName(t, manager, "fix the parser")
	projected, err := manager.BuildContext()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(projected.Messages) != 2 {
		t.Fatalf("context has %d messages, want the prompt and the answer", len(projected.Messages))
	}
}

// The window lists conversations by reading the files, so the name has to be
// the one a reader of that file finds, not just the one the app is holding.
func TestDerivedNameReachesTheFile(t *testing.T) {
	manager := newSession(t)
	autoName(t, manager, "fix the parser, then run the tests")
	info, err := session.ReadInfo(manager.File())
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	if info == nil {
		t.Fatal("the session file is not a session")
	}
	if got, want := info.Name, "fix the parser"; got != want {
		t.Fatalf("name on disk = %q, want %q", got, want)
	}
}

func TestSetNameSanitizes(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New())
	if err := application.SetName("  plan\n\nreview  "); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if got := application.Name(); got != "plan review" {
		t.Fatalf("name = %q, want %q", got, "plan review")
	}
	if got := manager.Name(); got != "plan review" {
		t.Fatalf("session name = %q, want %q", got, "plan review")
	}
	if application.Session() != manager {
		t.Fatal("app exposed a different session")
	}
}

func mustAppend(t *testing.T, manager *session.Manager, message wire.Message) string {
	t.Helper()
	id, err := manager.AppendMessage(message)
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	return id
}

func TestSteerIsInjectedBetweenTurns(t *testing.T) {
	block := newBlockingTool()
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "after steering"},
	)
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Tools = StaticTools([]tool.Tool{block})
	})

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := application.Ask(context.Background(), wire.UserMessage("start"), nil)
		done <- outcome{result, err}
	}()

	// The turn is inside the tool here, which is exactly when steering must
	// still be accepted.
	<-block.started
	application.Steer(wire.UserMessage("steer"))
	close(block.release)

	out := <-done
	if out.err != nil {
		t.Fatalf("ask: %v", out.err)
	}
	var texts []string
	for _, message := range out.result.Messages {
		texts = append(texts, message.Text())
	}
	want := []string{"start", "", "released", "steer", "after steering"}
	if len(texts) != len(want) {
		t.Fatalf("messages = %q, want %q", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("messages = %q, want %q", texts, want)
		}
	}

	calls := prov.Calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	found := false
	for _, message := range calls[1].Messages {
		if message.Role == wire.RoleUser && message.Text() == "steer" {
			found = true
		}
	}
	if !found {
		t.Fatal("second request did not carry the steering message")
	}
}

// A message queued while the app is idle waits for the next turn instead of
// being rejected: the queue outlives a turn, which is what lets a caller steer
// a conversation it has not started yet.
func TestSteerWhileIdleIsDeliveredOnNextTurn(t *testing.T) {
	prov := faux.New(faux.Response{Text: "answer"})
	application := newTestApp(t, newSession(t), prov)

	application.Steer(wire.UserMessage("too late"))
	if got := application.PendingCount(); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("start"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := application.PendingCount(); got != 0 {
		t.Fatalf("pending after ask = %d, want 0", got)
	}

	calls := prov.Calls()
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
	found := false
	for _, message := range calls[0].Messages {
		if message.Role == wire.RoleUser && message.Text() == "too late" {
			found = true
		}
	}
	if !found {
		t.Fatal("the first request did not carry the idle steering message")
	}
}

// A follow-up is delivered only when the agent would otherwise stop, so it
// extends the run instead of correcting it.
func TestFollowUpIsDeliveredAfterTheTurnStops(t *testing.T) {
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	application := newTestApp(t, newSession(t), prov)
	application.FollowUp(wire.UserMessage("follow-up"))

	result, err := application.Ask(context.Background(), wire.UserMessage("start"), nil)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	var texts []string
	for _, message := range result.Messages {
		texts = append(texts, message.Text())
	}
	want := []string{"start", "first", "follow-up", "second"}
	if len(texts) != len(want) {
		t.Fatalf("messages = %q, want %q", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("messages = %q, want %q", texts, want)
		}
	}
	if result.Text() != "second" {
		t.Fatalf("text = %q, want second", result.Text())
	}
	if calls := prov.Calls(); len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
}

func TestClearQueueReturnsDiscardedText(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	application.Steer(wire.UserMessage("steer"))
	application.FollowUp(wire.UserMessage("follow"))
	application.FollowUp(wire.UserMessage("follow again"))

	pendingSteering, pendingFollowUp := application.Pending()
	if len(pendingSteering) != 1 || pendingSteering[0].Text() != "steer" {
		t.Fatalf("pending steering = %+v, want the queued steer", pendingSteering)
	}
	if len(pendingFollowUp) != 2 {
		t.Fatalf("pending follow-up = %+v, want the queued follow-ups", pendingFollowUp)
	}

	steering, followUp := application.ClearQueue()
	if len(steering) != 1 || steering[0].Text() != "steer" {
		t.Fatalf("steering = %+v, want the discarded steer", steering)
	}
	if len(followUp) != 2 || followUp[0].Text() != "follow" || followUp[1].Text() != "follow again" {
		t.Fatalf("followUp = %+v, want the discarded follow-ups", followUp)
	}
	if got := application.PendingCount(); got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}

func TestQueueModeRoundTrips(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	if got := application.SteeringMode(); got != QueueModeOneAtATime {
		t.Fatalf("steering mode = %q, want the default", got)
	}
	if got := application.FollowUpMode(); got != QueueModeOneAtATime {
		t.Fatalf("follow-up mode = %q, want the default", got)
	}
	application.SetSteeringMode(QueueModeAll)
	application.SetFollowUpMode(QueueModeAll)
	if got := application.SteeringMode(); got != QueueModeAll {
		t.Fatalf("steering mode = %q, want %q", got, QueueModeAll)
	}
	if got := application.FollowUpMode(); got != QueueModeAll {
		t.Fatalf("follow-up mode = %q, want %q", got, QueueModeAll)
	}
}

// Reading the transcript while a turn is running must not race with the turn
// appending to it; the race detector in the gate is the assertion.
func TestMessagesDuringTurnIsRaceFree(t *testing.T) {
	block := newBlockingTool()
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "done"},
	)
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Tools = StaticTools([]tool.Tool{block})
	})

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("start"), nil)
		done <- err
	}()

	<-block.started
	stop := make(chan struct{})
	read := make(chan struct{})
	go func() {
		defer close(read)
		for {
			select {
			case <-stop:
				return
			default:
				application.Messages()
				application.MessageCount()
			}
		}
	}()
	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}
	close(stop)
	<-read

	messages := application.Messages()
	if got := application.MessageCount(); got != len(messages) {
		t.Fatalf("count = %d, want %d", got, len(messages))
	}
	if len(messages) != 4 {
		t.Fatalf("transcript = %d messages, want 4", len(messages))
	}
}

// A new session keeps the assistant but forgets the conversation, including the
// queue: a message queued for the old conversation must not leak into the new
// one.
func TestNewSessionStartsEmpty(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "answer"}, faux.Response{Text: "again"})
	application := newTestApp(t, manager, prov)

	oldID := manager.ID()
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	application.Steer(wire.UserMessage("queued"))
	application.SetSteeringMode(QueueModeAll)
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	// A side conversation belongs to the conversation that opened it.
	if _, err := application.BeginSide(); err != nil {
		t.Fatalf("begin side: %v", err)
	}

	if err := application.NewSession(); err != nil {
		t.Fatalf("new session: %v", err)
	}

	if got := manager.ID(); got == oldID {
		t.Fatalf("session id = %q, want a fresh one", got)
	}
	if got := application.MessageCount(); got != 0 {
		t.Fatalf("messages = %d, want an empty transcript", got)
	}
	if got := application.PendingCount(); got != 0 {
		t.Fatalf("pending = %d, want an empty queue", got)
	}
	if got := application.SteeringMode(); got != QueueModeOneAtATime {
		t.Fatalf("steering mode = %q, want the default", got)
	}
	if got := application.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking level = %q, want high to survive", got)
	}
	if leaf := manager.LeafID(); leaf != nil {
		t.Fatalf("leaf = %v, want none", *leaf)
	}
	if side := application.Side(); side != nil {
		t.Fatal("the side conversation outlived its session")
	}

	result, err := application.Ask(context.Background(), wire.UserMessage("again"), nil)
	if err != nil {
		t.Fatalf("ask after reset: %v", err)
	}
	if result.Text() != "again" {
		t.Fatalf("text = %q, want again", result.Text())
	}
	messages := application.Messages()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the prompt and the answer", len(messages))
	}
	if messages[0].Text() != "again" {
		t.Fatalf("first message = %q, want the new prompt", messages[0].Text())
	}
}

func TestNewSessionWhileBusy(t *testing.T) {
	prov := newGateProvider()
	application := newTestApp(t, newSession(t), prov)

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil)
		done <- err
	}()
	<-prov.started

	if err := application.NewSession(); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}

	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}
}

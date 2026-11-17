package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/provider/openai"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// pickerConfig declares the shapes the menu has to handle: a model without
// thinking, a reasoning model that names fewer gears than the protocol has, an
// undeclared reasoning model, and an id with a colon in it. The colon is what
// keeps ":level" un-ambiguous; the two reasoning rows are what keep the axis
// per model.
func pickerConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		DefaultModel: "test/m1",
		Providers: map[string]config.Provider{
			"test": {
				API:     openai.APIName,
				BaseURL: "http://127.0.0.1:1",
				APIKey:  "test-key",
				Models: []config.Model{
					{ID: "m1", Name: "M1", ContextWindow: 100000},
					// A vendor that collapses medium and xhigh onto high: the
					// axis it advertises is the subset, not seven names.
					{ID: "r1", Name: "R1", Reasoning: true, ThinkingLevels: []string{"max", "low", "high"}},
					// An undeclared reasoning model takes every gear.
					{ID: "r2", Name: "R2", Reasoning: true},
				},
			},
			"ollama": {
				API:     openai.APIName,
				BaseURL: "http://127.0.0.1:1",
				Models: []config.Model{
					{ID: "llama3:8b"},
					// A model whose id ends in something that looks like a
					// level: it exists so the precedence rule has a witness.
					{ID: "llama3:8b:high"},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// pickerApp builds a kernel session holding one of the declared models, which
// is what the menu reads its starting row and level from.
func pickerApp(t *testing.T, cfg *config.Config, providerName, modelID, thinking string) *app.App {
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
		Resolver:      resolver,
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application
}

// TestResolveSpecSplitsAnOptionalLevel pins the command syntax the menu fills
// in: "name[:level]".
func TestResolveSpecSplitsAnOptionalLevel(t *testing.T) {
	cfg := pickerConfig(t)
	cases := []struct {
		spec  string
		name  string
		level string
	}{
		{"test/m1", "test/m1", ""},
		{"m1", "test/m1", ""},
		{"test/r1:high", "test/r1", "high"},
		{"test/r1:off", "test/r1", "off"},
		// An undeclared model takes every level, so the protocol vocabulary is
		// the axis.
		{"test/r2:xhigh", "test/r2", "xhigh"},
		// Off is a switch, so even a model without thinking accepts it.
		{"test/m1:off", "test/m1", "off"},
		{"ollama/llama3:8b", "ollama/llama3:8b", ""},
		// The whole string names a model, so it wins over the suffix reading.
		{"ollama/llama3:8b:high", "ollama/llama3:8b:high", ""},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			selection, level, _, err := resolveSpec(cfg, tc.spec)
			if err != nil {
				t.Fatalf("resolveSpec(%q): %v", tc.spec, err)
			}
			if selection.Name() != tc.name || level != tc.level {
				t.Fatalf("resolveSpec(%q) = (%s, %q), want (%s, %q)", tc.spec, selection.Name(), level, tc.name, tc.level)
			}
		})
	}
}

// TestResolveSpecRejectsTheSuffixWhenTheModelIsMissing: a typo must name the
// model, not the level syntax the user did not invent.
func TestResolveSpecRejectsTheSuffixWhenTheModelIsMissing(t *testing.T) {
	cfg := pickerConfig(t)
	cases := []struct {
		spec string
		want string // what the message has to name
	}{
		// The suffix was split off, so the model half is the missing thing.
		{"test/nope:high", "nope"},
		{"nope", "nope"},
		// A name the protocol does not know is a bad level, not a bad model.
		{"test/m1:bogus", "bogus"},
		// A model with no axis cannot carry any level but off, and rounding
		// the request to off would hide that the model was the mistake.
		{"test/m1:high", "does not think"},
		// The colon id still splits, so the refusal is about the level.
		{"ollama/llama3:8b:low", "does not think"},
	}
	for _, tc := range cases {
		_, _, _, err := resolveSpec(cfg, tc.spec)
		if err == nil {
			t.Fatalf("resolveSpec(%q) = nil error, want a failure", tc.spec)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("resolveSpec(%q) error = %v, want %q named", tc.spec, err, tc.want)
		}
	}
}

// TestResolveSpecRoundsAGearTheModelDoesNotOffer: a real level outside the
// model's axis is a preference the table can round, and the note says what was
// sent instead.
func TestResolveSpecRoundsAGearTheModelDoesNotOffer(t *testing.T) {
	cfg := pickerConfig(t)
	cases := []struct {
		spec       string
		want       string
		adjustment string // what the note has to name
	}{
		// r1 offers off/low/high/max: ultra is one step above max.
		{"test/r1:ultra", "max", `"ultra"`},
		// medium is one step from low and one from high; the cheaper gear wins.
		{"test/r1:medium", "low", `"medium"`},
		{"test/r1:xhigh", "high", `"xhigh"`},
	}
	for _, tc := range cases {
		_, level, adjustment, err := resolveSpec(cfg, tc.spec)
		if err != nil {
			t.Fatalf("resolveSpec(%q): %v", tc.spec, err)
		}
		if level != tc.want {
			t.Fatalf("resolveSpec(%q) landed on %q, want %q", tc.spec, level, tc.want)
		}
		if !strings.Contains(adjustment, tc.adjustment) || !strings.Contains(adjustment, tc.want) {
			t.Fatalf("resolveSpec(%q) note = %q, want it to name %s and %q", tc.spec, adjustment, tc.adjustment, tc.want)
		}
	}
}

// TestThinkingLevelsAreFittedToTheModel covers the -thinking check: a name the
// protocol does not know and a model with no axis stay errors, and a real gear
// the model does not offer is rounded to the nearest one with a note.
func TestThinkingLevelsAreFittedToTheModel(t *testing.T) {
	cfg := pickerConfig(t)
	lookup := func(providerName, id string) config.Selection {
		selection, err := cfg.Lookup(providerName, id)
		if err != nil {
			t.Fatalf("lookup %s/%s: %v", providerName, id, err)
		}
		return selection
	}
	r1, r2, m1 := lookup("test", "r1"), lookup("test", "r2"), lookup("test", "m1")
	cases := []struct {
		selection  config.Selection
		level      string
		want       string
		adjustment bool
		wantErr    string // empty means accepted
	}{
		// Nothing asked for stays nothing.
		{r1, "", "", false, ""},
		{r1, "high", "high", false, ""},
		{r1, "off", "off", false, ""},
		{r2, "ultra", "ultra", false, ""},
		{m1, "off", "off", false, ""},
		// r1 offers off/low/high/max, so these three are rounded down or to the
		// nearer neighbour and the run says so.
		{r1, "ultra", "max", true, ""},
		{r1, "medium", "low", true, ""},
		{r1, "xhigh", "high", true, ""},
		// A model without an axis cannot carry a gear at all.
		{m1, "high", "", false, "does not think"},
		// A misspelling is not a preference: it never reaches the table.
		{r1, "minimal", "", false, "not one of"},
	}
	for _, tc := range cases {
		fitted, adjustment, err := assembly.FitThinking(tc.selection, tc.level)
		if tc.wantErr != "" {
			if err == nil {
				t.Fatalf("assembly.FitThinking(%s, %q) = nil error, want a failure", tc.selection.Name(), tc.level)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("assembly.FitThinking(%s, %q) error = %v, want %q", tc.selection.Name(), tc.level, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("assembly.FitThinking(%s, %q): %v", tc.selection.Name(), tc.level, err)
		}
		if fitted != tc.want {
			t.Fatalf("assembly.FitThinking(%s, %q) = %q, want %q", tc.selection.Name(), tc.level, fitted, tc.want)
		}
		if got := adjustment != ""; got != tc.adjustment {
			t.Fatalf("assembly.FitThinking(%s, %q) note = %q, want a note: %v", tc.selection.Name(), tc.level, adjustment, tc.adjustment)
		}
	}
}

// TestBareModelListsWhenThereIsNoMenu keeps the piped reader useful: with no
// editor to draw a menu in, the command still answers with the table.
func TestBareModelListsWhenThereIsNoMenu(t *testing.T) {
	cfg := pickerConfig(t)
	application := pickerApp(t, cfg, "test", "m1", "off")
	out := &strings.Builder{}
	listed := false
	model := models{list: func(w io.Writer) error {
		listed = true
		_, err := io.WriteString(w, "MODEL\n")
		return err
	}}

	if _, err := replCommand(context.Background(), application, nil, "/model", model, nil, nil, emptyPrompts(), out, nil, mcpState{}, nil, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	if !listed {
		t.Fatal("list was not called, want the table for a bare /model")
	}
	if !strings.Contains(out.String(), "current: M1") {
		t.Fatalf("output = %q, want the model in effect", out.String())
	}
}

// switchedModel is the /model switch built the way main.go builds it, so the
// menu test exercises the path the binary runs rather than a stub of it.
func switchedModel(t *testing.T, cfg *config.Config, application *app.App) func(string) (wire.Model, string, string, error) {
	t.Helper()
	return func(spec string) (wire.Model, string, string, error) {
		selection, level, adjustment, err := resolveSpec(cfg, spec)
		if err != nil {
			return wire.Model{}, "", "", err
		}
		model, err := application.SetModel(selection.ProviderName, selection.Model.ID)
		if err != nil {
			return wire.Model{}, "", "", err
		}
		return model, level, adjustment, nil
	}
}

// menuModels is the /model surface with a config behind it, which is what a
// terminal gets.
func menuModels(t *testing.T, cfg *config.Config, application *app.App) models {
	t.Helper()
	return models{
		switchModel: switchedModel(t, cfg, application),
		choices:     func() []tui.Choice { return modelChoices(cfg, application.ThinkingLevel()) },
	}
}

// TestBareModelOpensAMenu: with an interface to draw in, the bare command is a
// question and the answer is a list to move through.
func TestBareModelOpensAMenu(t *testing.T) {
	cfg := pickerConfig(t)
	application := pickerApp(t, cfg, "test", "m1", "off")
	out := &strings.Builder{}
	var asked tui.PickRequest
	pick := func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		return tui.PickAnswer{Value: "test/r1", Taken: true}
	}

	if _, err := replCommand(context.Background(), application, nil, "/model", menuModels(t, cfg, application), nil, pick, emptyPrompts(), out, nil, mcpState{}, nil, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	if asked.Title != "model" {
		t.Fatalf("the menu is titled %q", asked.Title)
	}
	if asked.Axis != thinkingAxis {
		t.Fatalf("the menu's axis = %q, want %q", asked.Axis, thinkingAxis)
	}
	// The list opens on the model in effect, so the menu says where the reader
	// is as well as what else there is.
	if asked.Current != "test/m1" {
		t.Fatalf("the menu opened on %q, want the model in effect", asked.Current)
	}
	items := asked.Items
	if len(items) != len(cfg.Selections()) {
		t.Fatalf("the menu offers %d of %d models", len(items), len(cfg.Selections()))
	}
	row := -1
	for i, item := range items {
		if item.Value == "test/m1" {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("the model in effect is not in the menu: %+v", items)
	}
	if want := "M1 (default)"; items[row].Label != want {
		t.Fatalf("that row reads %q, want %q", items[row].Label, want)
	}
	if !strings.Contains(items[row].Note, "ctx 100000") {
		t.Fatalf("that row's note = %q, want the context budget", items[row].Note)
	}
	// A model that cannot think has nothing to turn, and the axis collapses.
	if len(items[row].Steps) != 0 {
		t.Fatalf("a model without reasoning offers %v", items[row].Steps)
	}
	// Taking a row switches, and says so the way typing the name does.
	if !strings.Contains(out.String(), "model: R1") {
		t.Fatalf("output = %q, want the switch", out.String())
	}
	if got := currentModel(application); got != "test/r1" {
		t.Fatalf("model in effect = %q, want the picked one", got)
	}
}

// TestModelMenuTurningTheAxisSwitchesTheLevel: ←/→ on a row is half of that
// row's answer, and it travels with the model on one line so the switch cannot
// apply half of what was chosen.
func TestModelMenuTurningTheAxisSwitchesTheLevel(t *testing.T) {
	cfg := pickerConfig(t)
	application := pickerApp(t, cfg, "test", "m1", "off")
	out := &strings.Builder{}
	var asked tui.PickRequest
	pick := func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		for _, item := range req.Items {
			if item.Value != "test/r1" {
				continue
			}
			// What the interface would send after ←/→ landed on "max".
			at := slices.Index(item.Steps, "max")
			if at < 0 {
				t.Fatalf("r1 does not advertise max: %v", item.Steps)
			}
			return tui.PickAnswer{Value: item.Value, Step: at, Taken: true}
		}
		t.Fatal("r1 is not in the menu")
		return tui.PickAnswer{}
	}

	if _, err := replCommand(context.Background(), application, nil, "/model", menuModels(t, cfg, application), nil, pick, emptyPrompts(), out, nil, mcpState{}, nil, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	// The row advertises the subset this model declares, in the order of the
	// protocol's own list, with off in front because it is always available.
	for _, item := range asked.Items {
		if item.Value == "test/r1" {
			if got, want := strings.Join(item.Steps, " "), "off low high max"; got != want {
				t.Fatalf("r1's axis = %q, want %q", got, want)
			}
		}
	}
	if got := application.ThinkingLevel(); got != "max" {
		t.Fatalf("thinking level = %q, want the one the axis was left on", got)
	}
	if !strings.Contains(out.String(), "model: R1 (thinking: max)") {
		t.Fatalf("output = %q, want the switch and the level", out.String())
	}
}

// TestModelMenuWithoutAnAxisSwitchesTheModelAlone: a row with nothing to turn
// answers the model and nothing else, so the switch cannot invent a level for a
// model that has none.
func TestModelMenuWithoutAnAxisSwitchesTheModelAlone(t *testing.T) {
	cfg := pickerConfig(t)
	application := pickerApp(t, cfg, "test", "r1", "high")
	out := &strings.Builder{}
	pick := func(req tui.PickRequest) tui.PickAnswer {
		return tui.PickAnswer{Value: "test/m1", Taken: true}
	}

	if _, err := replCommand(context.Background(), application, nil, "/model", menuModels(t, cfg, application), nil, pick, emptyPrompts(), out, nil, mcpState{}, nil, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "model: M1\n") {
		t.Fatalf("output = %q, want the switch with no level appended", got)
	}
	if got := currentModel(application); got != "test/m1" {
		t.Fatalf("model in effect = %q, want the picked one", got)
	}
}

// TestModelMenuOpensTheAxisAtTheLevelInEffect: the row that is already switched
// to opens on the position the session holds, not on its first gear.
func TestModelMenuOpensTheAxisAtTheLevelInEffect(t *testing.T) {
	cfg := pickerConfig(t)
	application := pickerApp(t, cfg, "test", "r1", "high")
	var asked tui.PickRequest
	pick := func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		return tui.PickAnswer{}
	}

	if _, err := replCommand(context.Background(), application, nil, "/model", menuModels(t, cfg, application), nil, pick, emptyPrompts(), &strings.Builder{}, nil, mcpState{}, nil, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	for _, item := range asked.Items {
		if item.Value != "test/r1" {
			continue
		}
		if at := item.Steps[item.Step]; at != "high" {
			t.Fatalf("r1's row opened on %q, want the level in effect", at)
		}
	}
}

// TestClosingTheModelMenuChangesNothing: the reader asked to look, and looking
// is not a switch.
func TestClosingTheModelMenuChangesNothing(t *testing.T) {
	cfg := pickerConfig(t)
	application := pickerApp(t, cfg, "test", "m1", "off")
	out := &strings.Builder{}
	pick := func(tui.PickRequest) tui.PickAnswer { return tui.PickAnswer{} }

	if _, err := replCommand(context.Background(), application, nil, "/model", menuModels(t, cfg, application), nil, pick, emptyPrompts(), out, nil, mcpState{}, nil, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing said about a menu that was closed", out.String())
	}
	if got := currentModel(application); got != "test/m1" {
		t.Fatalf("model in effect = %q, want it left alone", got)
	}
}

package catalog

import (
	"slices"
	"strings"
	"testing"
)

// seedIDs is the embedded table's inventory. Every data test below reads from
// Default() instead of a fixture, so a change to models.json that breaks an
// assumption fails here rather than in whichever adapter happens to load first.
var seedIDs = []string{
	"claude-haiku-4-5",
	"claude-opus-4-5",
	"claude-sonnet-4-6",
	"deepseek-flash",
	"deepseek-v4-pro",
	"gpt-4.1",
	"gpt-4o",
	"gpt-5",
	"gpt-5-mini",
}

func mustEntry(t *testing.T, table *Table, id string) Entry {
	t.Helper()
	entry, ok := table.Lookup(id)
	if !ok {
		t.Fatalf("Lookup(%q) found nothing", id)
	}
	return entry
}

func TestLevelsAreTheVocabulary(t *testing.T) {
	want := []Level{"off", "low", "medium", "high", "xhigh", "max", "ultra"}
	if len(Levels) != len(want) {
		t.Fatalf("Levels has %d entries, want %d", len(Levels), len(want))
	}
	for i, level := range want {
		if Levels[i] != level {
			t.Errorf("Levels[%d] = %q, want %q", i, Levels[i], level)
		}
		if !ValidLevel(string(level)) {
			t.Errorf("ValidLevel(%q) = false", level)
		}
	}
	for _, name := range []string{"", "minimal", "none", "HIGH", " high", "high ", "off ", "turbo"} {
		if ValidLevel(name) {
			t.Errorf("ValidLevel(%q) = true, want false", name)
		}
	}
}

func TestClamp(t *testing.T) {
	cases := []struct {
		name    string
		offered []Level
		want    Level
		got     Level
	}{
		{"nothing offered is a model that cannot think", nil, LevelHigh, LevelOff},
		{"off is a switch and passes through", []Level{LevelOff, LevelLow}, LevelOff, LevelOff},
		{"an offered gear is untouched", []Level{LevelOff, LevelLow, LevelMedium, LevelHigh}, LevelMedium, LevelMedium},
		{"above the top clamps to the top", []Level{LevelOff, LevelLow, LevelMedium, LevelHigh}, LevelUltra, LevelHigh},
		{"the nearest neighbour wins when only it is lower", []Level{LevelOff, LevelLow}, LevelHigh, LevelLow},
		{"a tie takes the cheaper gear", []Level{LevelOff, LevelLow, LevelHigh}, LevelMedium, LevelLow},
		{"a tie ignores the order of offered", []Level{LevelHigh, LevelLow, LevelOff}, LevelMedium, LevelLow},
		{"gears the vocabulary does not know are skipped", []Level{LevelOff, Level("bogus"), LevelHigh}, LevelHigh, LevelHigh},
		{"only off offered", []Level{LevelOff}, LevelMax, LevelOff},
		{"an unknown request becomes off", Levels, Level("turbo"), LevelOff},
		{"every gear offered keeps the request", Levels, LevelUltra, LevelUltra},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(c.offered, c.want); got != c.got {
				t.Errorf("Clamp(%v, %q) = %q, want %q", c.offered, c.want, got, c.got)
			}
		})
	}
}

func TestGears(t *testing.T) {
	table := Default()
	cases := []struct {
		id    string
		gears []Level
	}{
		// Haiku declares no narrowing, so it offers the whole axis.
		{"claude-haiku-4-5", Levels},
		{"claude-opus-4-5", []Level{LevelOff, LevelLow, LevelMedium, LevelHigh}},
		{"claude-sonnet-4-6", []Level{LevelOff, LevelLow, LevelMedium, LevelHigh, LevelMax}},
		{"deepseek-v4-pro", []Level{LevelOff, LevelLow, LevelHigh, LevelMax}},
		// A model that cannot think has no gear axis at all.
		{"gpt-4o", nil},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := mustEntry(t, table, c.id).Gears(); !slices.Equal(got, c.gears) {
				t.Errorf("Gears() = %v, want %v", got, c.gears)
			}
		})
	}
}

func TestAllowsGear(t *testing.T) {
	table := Default()
	thinker := mustEntry(t, table, "gpt-5")
	plain := mustEntry(t, table, "gpt-4o")
	wide := mustEntry(t, table, "claude-haiku-4-5")

	if !thinker.AllowsGear(LevelMedium) {
		t.Error("gpt-5 rejects a gear it declares")
	}
	if thinker.AllowsGear(LevelXHigh) {
		t.Error("gpt-5 accepts xhigh, which it does not declare")
	}
	if !plain.AllowsGear(LevelOff) {
		t.Error("a model that cannot think has to accept off")
	}
	if plain.AllowsGear(LevelLow) {
		t.Error("gpt-4o accepts low, but it cannot think")
	}
	if !wide.AllowsGear(LevelUltra) {
		t.Error("a model that declares no narrowing rejects ultra")
	}
}

func TestEntryClamp(t *testing.T) {
	table := Default()
	if got := mustEntry(t, table, "gpt-4o").Clamp(LevelUltra); got != LevelOff {
		t.Errorf("gpt-4o clamped ultra to %q, want off", got)
	}
	if got := mustEntry(t, table, "gpt-5").Clamp(LevelUltra); got != LevelHigh {
		t.Errorf("gpt-5 clamped ultra to %q, want high", got)
	}
	if got := mustEntry(t, table, "claude-haiku-4-5").Clamp(LevelUltra); got != LevelUltra {
		t.Errorf("haiku clamped ultra to %q, want ultra", got)
	}
}

func TestLookup(t *testing.T) {
	table := Default()
	if got := table.Len(); got != len(seedIDs) {
		t.Fatalf("Len() = %d, want %d", got, len(seedIDs))
	}
	for _, id := range seedIDs {
		entry := mustEntry(t, table, id)
		if entry.ID != id {
			t.Errorf("Lookup(%q).ID = %q", id, entry.ID)
		}
	}
	// Exact matching only: a case-insensitive hit would send the spelling the
	// user typed to the endpoint, and the qualified form is the config's
	// business, not this table's.
	for _, id := range []string{"", "GPT-5", "gpt-5 ", " gpt-5", "openai/gpt-5", "gpt-5.1"} {
		if entry, ok := table.Lookup(id); ok {
			t.Errorf("Lookup(%q) found %q", id, entry.ID)
		}
	}
}

func TestAllIsSorted(t *testing.T) {
	entries := Default().All()
	for i := 1; i < len(entries); i++ {
		previous, current := entries[i-1], entries[i]
		if previous.Provider > current.Provider ||
			(previous.Provider == current.Provider && previous.ID >= current.ID) {
			t.Errorf("All() is out of order at %d: %s/%s then %s/%s",
				i, previous.Provider, previous.ID, current.Provider, current.ID)
		}
	}
}

func TestAllReturnsCopies(t *testing.T) {
	table := Default()
	entries := table.All()
	entries[0].Name = "clobbered"
	entries[0].Input[0] = "clobbered"

	again := table.All()
	if again[0].Name == "clobbered" {
		t.Error("All() let a caller rewrite a stored field")
	}
	if again[0].Input[0] == "clobbered" {
		t.Error("All() let a caller write through a stored slice")
	}

	entry := mustEntry(t, table, "gpt-5")
	entry.Thinking[0] = LevelUltra
	if got := mustEntry(t, table, "gpt-5").Thinking[0]; got != LevelLow {
		t.Errorf("Lookup() let a caller write through a stored slice: thinking[0] = %q", got)
	}
}

func TestSeedData(t *testing.T) {
	table := Default()
	source, snapshot := table.Provenance()
	if source == "" || snapshot == "" {
		t.Fatalf("Provenance() = %q, %q; both have to be set", source, snapshot)
	}

	// The numbers are the point of the file, so they are pinned here: a typo in
	// models.json that a reader would not notice is a request built for the wrong
	// window.
	cases := []struct {
		id        string
		provider  string
		name      string
		context   int
		maxTokens int
		reasoning bool
		input     []string
	}{
		{"claude-haiku-4-5", "anthropic", "Claude Haiku 4.5", 200000, 64000, true, []string{"text", "image"}},
		{"claude-opus-4-5", "anthropic", "Claude Opus 4.5", 200000, 64000, true, []string{"text", "image"}},
		{"claude-sonnet-4-6", "anthropic", "Claude Sonnet 4.6", 1000000, 128000, true, []string{"text", "image"}},
		{"deepseek-flash", "deepseek", "DeepSeek V4.1 Flash", 1048576, 393216, true, []string{"text", "image"}},
		{"deepseek-v4-pro", "deepseek", "DeepSeek V4 Pro", 1048576, 393216, true, []string{"text"}},
		{"gpt-4.1", "openai", "GPT-4.1", 1047576, 32768, false, []string{"text", "image"}},
		{"gpt-4o", "openai", "GPT-4o", 128000, 16384, false, []string{"text", "image"}},
		{"gpt-5", "openai", "GPT-5", 400000, 128000, true, []string{"text", "image"}},
		{"gpt-5-mini", "openai", "GPT-5 mini", 400000, 128000, true, []string{"text", "image"}},
	}
	if len(cases) != len(seedIDs) {
		t.Fatalf("the table has %d models, this test pins %d", len(cases), len(seedIDs))
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			entry := mustEntry(t, table, c.id)
			if entry.Provider != c.provider || entry.Name != c.name {
				t.Errorf("provider/name = %q/%q, want %q/%q", entry.Provider, entry.Name, c.provider, c.name)
			}
			if entry.ContextWindow != c.context || entry.MaxTokens != c.maxTokens {
				t.Errorf("window = %d/%d, want %d/%d", entry.ContextWindow, entry.MaxTokens, c.context, c.maxTokens)
			}
			if entry.Reasoning != c.reasoning {
				t.Errorf("reasoning = %v, want %v", entry.Reasoning, c.reasoning)
			}
			if !slices.Equal(entry.Input, c.input) {
				t.Errorf("input = %v, want %v", entry.Input, c.input)
			}
		})
	}
}

// goodModel is a valid entry the reject tests edit one field at a time.
const goodModel = `{"provider":"openai","id":"gpt-5","name":"GPT-5","contextWindow":400000,"maxTokens":128000,"reasoning":true,"input":["text","image"],"thinking":["low","medium","high"]}`

func docJSON(models ...string) string {
	return `{"source":"https://models.dev/api.json","snapshot":"2026-11-14","models":[` + strings.Join(models, ",") + `]}`
}

func TestLoadAcceptsAValidDocument(t *testing.T) {
	table, err := Load([]byte(docJSON(goodModel)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if table.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", table.Len())
	}
	entry := mustEntry(t, table, "gpt-5")
	if entry.ContextWindow != 400000 || entry.MaxTokens != 128000 {
		t.Errorf("window = %d/%d", entry.ContextWindow, entry.MaxTokens)
	}
	source, snapshot := table.Provenance()
	if source != "https://models.dev/api.json" || snapshot != "2026-11-14" {
		t.Errorf("Provenance() = %q, %q", source, snapshot)
	}
}

// TestLoadRejects covers every rule validate enforces. The cases are written by
// editing one field of a valid document, so a rule that stops being checked
// shows up as a case that suddenly loads.
func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"not json", `not a table`},
		{"unknown key", docJSON(strings.Replace(goodModel, `"name":"GPT-5"`, `"name":"GPT-5","cost":3`, 1))},
		{"trailing data", docJSON(goodModel) + ` {}`},
		{"no source", `{"snapshot":"2026-11-14","models":[` + goodModel + `]}`},
		{"no snapshot", `{"source":"https://models.dev/api.json","models":[` + goodModel + `]}`},
		{"no models", `{"source":"https://models.dev/api.json","snapshot":"2026-11-14","models":[]}`},
		{"no provider", docJSON(strings.Replace(goodModel, `"provider":"openai",`, ``, 1))},
		{"no id", docJSON(strings.Replace(goodModel, `"id":"gpt-5",`, ``, 1))},
		{"id with a space", docJSON(strings.Replace(goodModel, `"id":"gpt-5"`, `"id":"gpt 5"`, 1))},
		{"id with a slash", docJSON(strings.Replace(goodModel, `"id":"gpt-5"`, `"id":"openai/gpt-5"`, 1))},
		{"id with a control character", docJSON(strings.Replace(goodModel, `"id":"gpt-5"`, `"id":"gpt\u00005"`, 1))},
		{"zero window", docJSON(strings.Replace(goodModel, `"contextWindow":400000`, `"contextWindow":0`, 1))},
		{"zero output", docJSON(strings.Replace(goodModel, `"maxTokens":128000`, `"maxTokens":0`, 1))},
		{"no room for a prompt", docJSON(strings.Replace(goodModel, `"maxTokens":128000`, `"maxTokens":400000`, 1))},
		{"unknown modality", docJSON(strings.Replace(goodModel, `"image"`, `"audio"`, 1))},
		{"duplicate modality", docJSON(strings.Replace(goodModel, `["text","image"]`, `["text","text"]`, 1))},
		{"thinking without reasoning", docJSON(strings.Replace(goodModel, `"reasoning":true`, `"reasoning":false`, 1))},
		{"off in thinking", docJSON(strings.Replace(goodModel, `"thinking":["low"`, `"thinking":["off","low"`, 1))},
		{"duplicate gear", docJSON(strings.Replace(goodModel, `"medium"`, `"low"`, 1))},
		{"unknown gear", docJSON(strings.Replace(goodModel, `"medium"`, `"minimal"`, 1))},
		{
			// The qualified form is how a config disambiguates; two providers
			// claiming one bare id would make Lookup a coin flip.
			"one id, two providers",
			docJSON(goodModel, strings.Replace(goodModel, `"provider":"openai"`, `"provider":"deepseek"`, 1)),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table, err := Load([]byte(c.data))
			if err == nil {
				t.Fatalf("Load accepted %s: %d entries", c.name, table.Len())
			}
			if !strings.HasPrefix(err.Error(), "catalog:") {
				t.Errorf("error %q does not start with the package name", err)
			}
		})
	}
}

func TestLoadNormalizesGears(t *testing.T) {
	data := docJSON(strings.Replace(goodModel, `["low","medium","high"]`, `["high","low","medium"]`, 1))
	table, err := Load([]byte(data))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := mustEntry(t, table, "gpt-5").Thinking, []Level{LevelLow, LevelMedium, LevelHigh}; !slices.Equal(got, want) {
		t.Errorf("Thinking = %v, want %v", got, want)
	}
}

func TestLoadSortsByProviderThenID(t *testing.T) {
	anthropic := strings.Replace(
		strings.Replace(goodModel, `"provider":"openai"`, `"provider":"anthropic"`, 1),
		`"id":"gpt-5"`, `"id":"claude-x"`, 1)
	// The data lists openai first; All has to list anthropic first.
	table, err := Load([]byte(docJSON(goodModel, anthropic)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := table.All()
	if len(entries) != 2 || entries[0].ID != "claude-x" || entries[1].ID != "gpt-5" {
		t.Fatalf("All() = %v, want claude-x then gpt-5", entries)
	}
}

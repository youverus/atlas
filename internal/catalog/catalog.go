// Package catalog is the model directory: what each model can do, looked up by
// its id.
//
// The table is embedded JSON (models.json) rather than generated Go, so that a
// wrong window or a missing gear is a reviewable data diff and the file can be
// regenerated from models.dev without touching logic. Nothing here touches the
// network or the filesystem at lookup time: a table is parsed once, validated
// as a whole, and then only read.
//
// The package is deliberately a leaf. It repeats the thinking vocabulary
// instead of importing internal/config, and it never mentions internal/wire:
// turning an Entry into a wire.Model is one struct literal in cmd/atlas, and
// keeping that out of here is what lets the table be tested with no kernel, no
// config file and no adapter.
package catalog

// Entry is one model. The fields are the facts an adapter or a status line
// needs to answer "can this endpoint take this request", and nothing about how
// a particular dialect spells them.
type Entry struct {
	// Provider is the endpoint family that serves the model: "anthropic",
	// "openai", "deepseek". It selects the adapter, so it is not free text.
	Provider string `json:"provider"`

	// ID is the model name an endpoint expects.
	ID string `json:"id"`

	// Name is what a status line shows. It is display-only; nothing routes on
	// it.
	Name string `json:"name"`

	// ContextWindow is the request limit in tokens, and MaxTokens the response
	// limit. Both are what the vendor documents, not what a conservative client
	// would use: trimming them for safety belongs to whoever builds the request.
	ContextWindow int `json:"contextWindow"`
	MaxTokens     int `json:"maxTokens"`

	// Reasoning is true when the model can be told to think. It is what decides
	// whether there is a gear axis at all.
	Reasoning bool `json:"reasoning"`

	// Input lists the modalities the model accepts. Only "text" and "image" are
	// allowed: the kernel's content types are exactly those two, and declaring
	// anything else would let an adapter drop data it promised to send.
	Input []string `json:"input"`

	// Thinking narrows the gears the model offers, in ascending effort. Empty
	// means "not narrowed" and therefore every gear, matching
	// config.Model.ThinkingLevels. It is only meaningful when Reasoning is true,
	// and it never contains off: off is a switch, not a gear.
	Thinking []Level `json:"thinking"`
}

// Gears returns the gears this model offers, in canonical order: off plus
// whichever efforts it declares. A reasoning model that declares nothing offers
// every gear; a model that cannot think has no axis at all.
func (e Entry) Gears() []Level {
	if !e.Reasoning {
		return nil
	}
	declared := make(map[Level]bool, len(e.Thinking))
	for _, level := range e.Thinking {
		declared[level] = true
	}
	gears := make([]Level, 0, len(Levels))
	for _, level := range Levels {
		if len(declared) == 0 || level == LevelOff || declared[level] {
			gears = append(gears, level)
		}
	}
	return gears
}

// AllowsGear reports whether the model accepts level. Off is always accepted:
// turning thinking off is a switch, and a model that cannot think still has to
// be told so.
func (e Entry) AllowsGear(level Level) bool {
	if level == LevelOff {
		return true
	}
	for _, gear := range e.Gears() {
		if gear == level {
			return true
		}
	}
	return false
}

// Clamp maps want onto a gear this model offers.
func (e Entry) Clamp(want Level) Level {
	return Clamp(e.Gears(), want)
}

// clone keeps a caller from writing through the slices it was handed.
func (e Entry) clone() Entry {
	e.Input = append([]string(nil), e.Input...)
	e.Thinking = append([]Level(nil), e.Thinking...)
	return e
}

// Table is a validated set of entries with an index by id. It is read-only
// after Load; every method returns copies so that a caller cannot change what
// the next caller sees.
type Table struct {
	// source and snapshot are the data file's provenance, kept because the first
	// question about a wrong window is which numbers, from when.
	source   string
	snapshot string

	// entries is sorted by provider, then id, so that All is stable and a diff
	// of two tables is readable.
	entries []Entry

	// index maps a bare model id to its position in entries. A bare id is
	// unique across the table, which Load enforces: "provider/model" is how a
	// config disambiguates, and that resolution needs the config's rules, so it
	// stays out of this package.
	index map[string]int
}

// Lookup returns the entry for id. The match is exact: "GPT-5" does not find
// "gpt-5", because an id that came back from a case-insensitive search would
// then be sent to the endpoint as the spelling the user typed.
func (t *Table) Lookup(id string) (Entry, bool) {
	i, ok := t.index[id]
	if !ok {
		return Entry{}, false
	}
	return t.entries[i].clone(), true
}

// All returns every entry, sorted by provider then id.
func (t *Table) All() []Entry {
	entries := make([]Entry, len(t.entries))
	for i, entry := range t.entries {
		entries[i] = entry.clone()
	}
	return entries
}

// Len returns the number of entries.
func (t *Table) Len() int {
	return len(t.entries)
}

// Provenance returns where the table's numbers came from and when they were
// read.
func (t *Table) Provenance() (source, snapshot string) {
	return t.source, t.snapshot
}

// Default returns the embedded table. It is parsed and validated while the
// package initialises, so a broken models.json stops the binary at startup
// instead of quietly becoming "no model metadata", which would turn automatic
// compaction off without anybody noticing.
func Default() *Table {
	return builtin
}

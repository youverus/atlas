package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
)

// modelsJSON is the seed table: the models this build can name with confidence,
// not a mirror of the upstream catalog. Adding a model is a data edit; reading
// the whole of models.dev is the planned cmd/atlas-catalog generator's job, and
// keeping the hand-picked file reviewable is why it is small.
//
//go:embed models.json
var modelsJSON []byte

// builtin is the parsed embedded table. Panicking is deliberate: a table that
// failed validation means the numbers every request is built from are wrong, and
// stopping at startup is the only outcome that cannot be misread as "this model
// has no metadata" — which would turn automatic compaction off in silence.
var builtin = mustLoad(modelsJSON)

func mustLoad(data []byte) *Table {
	table, err := Load(data)
	if err != nil {
		panic(fmt.Sprintf("catalog: embedded models.json is invalid: %v", err))
	}
	return table
}

// document is the file shape. Both header fields are required so that a stale
// table says where its numbers came from and when they were read.
type document struct {
	Source   string  `json:"source"`
	Snapshot string  `json:"snapshot"`
	Models   []Entry `json:"models"`
}

// Load parses and validates a table.
//
// Decoding is strict both ways: an unknown key is an error, because a typo in
// "contextWindow" would otherwise be read as a zero window, and so is anything
// after the document. Validation finishes before the table is handed out, so
// every method on Table can assume a well formed entry.
func Load(data []byte) (*Table, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("catalog: trailing data after the JSON document")
	}

	if doc.Source == "" {
		return nil, errors.New("catalog: source is empty; the table has to say where its numbers came from")
	}
	if doc.Snapshot == "" {
		return nil, errors.New("catalog: snapshot is empty; the table has to say when its numbers were read")
	}
	if len(doc.Models) == 0 {
		return nil, errors.New("catalog: models is empty")
	}

	entries := make([]Entry, len(doc.Models))
	for i, entry := range doc.Models {
		if err := entry.validate(); err != nil {
			return nil, fmt.Errorf("catalog: models[%d] %q: %w", i, entry.ID, err)
		}
		entries[i] = entry.normalize()
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].ID < entries[j].ID
	})

	index := make(map[string]int, len(entries))
	for i, entry := range entries {
		if _, taken := index[entry.ID]; taken {
			return nil, fmt.Errorf("catalog: model id %q is declared more than once; a bare id has to name exactly one model", entry.ID)
		}
		index[entry.ID] = i
	}

	return &Table{source: doc.Source, snapshot: doc.Snapshot, entries: entries, index: index}, nil
}

// validate rejects an entry that would make a later lookup or clamp lie.
func (e Entry) validate() error {
	if e.Provider == "" {
		return errors.New("provider is empty")
	}
	if e.ID == "" {
		return errors.New("id is empty")
	}
	if strings.ContainsFunc(e.ID, func(r rune) bool {
		return r == '/' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return fmt.Errorf("id %q contains a slash, space or control character; an id is what the endpoint expects, and a slash would make the provider/id form ambiguous", e.ID)
	}
	if e.ContextWindow <= 0 {
		return fmt.Errorf("contextWindow is %d; it has to be positive", e.ContextWindow)
	}
	if e.MaxTokens <= 0 {
		return fmt.Errorf("maxTokens is %d; it has to be positive", e.MaxTokens)
	}
	if e.MaxTokens >= e.ContextWindow {
		return fmt.Errorf("maxTokens %d leaves no room for a prompt in a contextWindow of %d", e.MaxTokens, e.ContextWindow)
	}

	seenInput := make(map[string]bool, len(e.Input))
	for _, input := range e.Input {
		switch input {
		case "text", "image":
		default:
			return fmt.Errorf("input %q is not a modality the kernel can carry; allowed: text, image", input)
		}
		if seenInput[input] {
			return fmt.Errorf("input %q is listed twice", input)
		}
		seenInput[input] = true
	}

	if !e.Reasoning {
		if len(e.Thinking) > 0 {
			return fmt.Errorf("thinking lists %d levels but reasoning is false; a model that cannot think has no gear axis", len(e.Thinking))
		}
		return nil
	}

	seenLevel := make(map[Level]bool, len(e.Thinking))
	for _, level := range e.Thinking {
		if level == LevelOff {
			return errors.New("thinking contains off; off is a switch that is always available, so listing it declares nothing")
		}
		if rank(level) < 0 {
			names := make([]string, len(Levels)-1)
			for i, known := range Levels[1:] {
				names[i] = string(known)
			}
			return fmt.Errorf("thinking contains %q, which is not one of: %s", level, strings.Join(names, ", "))
		}
		if seenLevel[level] {
			return fmt.Errorf("thinking lists %q twice", level)
		}
		seenLevel[level] = true
	}
	return nil
}

// normalize puts the declared gears in canonical order. Gears ignores the order,
// but All hands the raw slice out, and two tables that differ only in how their
// data file was typed should compare equal.
func (e Entry) normalize() Entry {
	if len(e.Thinking) < 2 {
		return e
	}
	sorted := make([]Level, len(e.Thinking))
	copy(sorted, e.Thinking)
	sort.Slice(sorted, func(i, j int) bool { return rank(sorted[i]) < rank(sorted[j]) })
	e.Thinking = sorted
	return e
}

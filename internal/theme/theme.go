// Package theme reads the palettes atlas draws with.
//
// A theme is a JSON file of semantic tokens: the interface asks for "accent"
// or "error", never for a color. The indirection is the whole point. A palette
// becomes one file to edit instead of a hundred hex literals, and a light
// terminal needs a different file rather than a different build.
//
// Three rules follow from what a palette is used for:
//
//   - A missing token is an error, and the error names every missing token and
//     the file it came from. A silently defaulted color is how a theme ends up
//     half applied without anyone noticing: the half that defaulted still
//     renders, so nothing looks broken.
//   - Unknown fields and unknown token names are rejected. A misspelled
//     "acccent" would otherwise parse, be ignored, and leave accent at
//     whatever the base theme said.
//   - Nothing is looked up implicitly. The caller names the directories, so a
//     test never reads ~/.atlas/themes by accident.
//
// The package is pure: it reads files it is told about, and the only thing it
// writes is nothing at all.
package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Version is the schema this build reads. It is written into every theme file
// and checked on the way in: a version bump that changes what a token means
// must not be readable by an older binary that would guess.
const Version = 1

// maxSize bounds one theme file. A palette is a dozen lines; anything larger
// is a mistake, and refusing early keeps a decode from streaming an unbounded
// file into memory.
const maxSize = 64 << 10

// maxExtends bounds an inheritance chain. The limit is not about size: a chain
// deep enough to reach it is a chain nobody can read.
const maxExtends = 8

// DefaultColor is the value that leaves the choice to the terminal. It is a
// value, not an absent one: a background of "default" means "paint nothing and
// let the terminal show through", which is what a transparent theme needs, and
// it is a different thing from a black background.
const DefaultColor = "default"

// Mode is what the palette was designed against. It is written down rather
// than guessed, because a theme is chosen by name and the light one has to be
// findable without sampling a color and hoping.
type Mode string

const (
	// Dark is a palette for a dark terminal background.
	Dark Mode = "dark"
	// Light is a palette for a light terminal background.
	Light Mode = "light"
)

// Token names one semantic slot in the palette.
type Token string

const (
	// TokenText is ordinary text: what the model said, what was typed.
	TokenText Token = "text"
	// TokenDim is secondary text: hints, key names, timestamps, and anything
	// the eye should be able to skip.
	TokenDim Token = "dim"
	// TokenAccent marks titles, prompts, and the focused control.
	TokenAccent Token = "accent"
	// TokenBorder draws the frames around boxes and panels.
	TokenBorder Token = "border"
	// TokenOK reports a call that ran.
	TokenOK Token = "ok"
	// TokenWarn reports something the user should read but need not act on.
	TokenWarn Token = "warn"
	// TokenError reports a failure. It is spelled out rather than abbreviated
	// so a theme reads like prose.
	TokenError Token = "error"
	// TokenUser marks what the user said, so a transcript can be skimmed.
	TokenUser Token = "user"
	// TokenTool marks a tool call and its name.
	TokenTool Token = "tool"
	// TokenCode covers inline code, fenced blocks, and diffs.
	TokenCode Token = "code"
	// TokenSelection is the background of a selected row in a list.
	TokenSelection Token = "selection"
	// TokenBackground is the app background. DefaultColor leaves the terminal's
	// own background in place.
	TokenBackground Token = "background"
)

// Tokens lists every token, in the order the documentation presents them. A
// theme has to name all of them, so this is also the completeness checklist.
var Tokens = []Token{
	TokenText, TokenDim, TokenAccent, TokenBorder,
	TokenOK, TokenWarn, TokenError,
	TokenUser, TokenTool, TokenCode,
	TokenSelection, TokenBackground,
}

// Color is one token's value: a hex color, or the terminal's own default.
//
// Hex is normalised on the way in — "#ABC" becomes "#aabbcc" — so a theme, a
// rendered frame, and a test golden all spell the same color the same way.
type Color struct {
	hex   string
	isdef bool
}

// Default reports whether the color leaves the choice to the terminal.
func (c Color) Default() bool { return c.isdef }

// Hex returns "#rrggbb", or "" for the terminal default.
func (c Color) Hex() string { return c.hex }

// String is the value as a theme file spells it, which is what a diagnostic
// and a test golden both want to read.
func (c Color) String() string {
	if c.isdef {
		return DefaultColor
	}
	return c.hex
}

// parseColor reads one token value.
func parseColor(raw string) (Color, error) {
	if raw == DefaultColor {
		return Color{isdef: true}, nil
	}
	if !strings.HasPrefix(raw, "#") {
		return Color{}, fmt.Errorf("%q is not \"%s\" or a hex color like \"#7aa2f7\"", raw, DefaultColor)
	}
	digits := raw[1:]
	switch len(digits) {
	case 3:
		// #abc is #aabbcc; expanding here means every later reader, including
		// lipgloss, sees one form.
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2]})
	case 6:
	default:
		return Color{}, fmt.Errorf("%q has %d digits after \"#\"; a color is 3 or 6", raw, len(digits))
	}
	for _, digit := range digits {
		switch {
		case digit >= '0' && digit <= '9':
		case digit >= 'a' && digit <= 'f':
		case digit >= 'A' && digit <= 'F':
		default:
			return Color{}, fmt.Errorf("%q is not a hex color: %q is not a hex digit", raw, string(digit))
		}
	}
	return Color{hex: "#" + strings.ToLower(digits)}, nil
}

// Palette is the resolved set of colors. It is a struct rather than a map so
// that a token the interface asks for always exists: completeness is checked
// once, on the way in, instead of at every call site.
type Palette struct {
	Text       Color
	Dim        Color
	Accent     Color
	Border     Color
	OK         Color
	Warn       Color
	Error      Color
	User       Color
	Tool       Color
	Code       Color
	Selection  Color
	Background Color
}

// Theme is one named palette.
type Theme struct {
	// Name is the file name without its extension: it is what a config names
	// and what /theme lists.
	Name string
	// Mode is what the palette was drawn for.
	Mode Mode
	// Palette is the resolved colors, complete by construction.
	Palette Palette
}

// surfaceLift is how far a raised surface sits from the screen color, as a
// fraction of the distance to white on a dark palette or to black on a light
// one. Eight percent is a step the eye reads as "this panel is above the
// screen" without reading as a different color: at the bottom of a dark
// palette it is about eighteen levels per channel, which a 24-bit terminal
// shows plainly and a 256-color approximation keeps as well.
const surfaceLift = 0.08

// Surface is the background for a panel that sits above the screen: today, the
// box around the prompt.
//
// It is derived rather than declared as a thirteenth token because it is a
// relationship, not a choice. Two tokens would allow a theme whose "raised"
// panel is darker than the screen it rises from, and the reader would have to
// notice a mistake that can be computed away. The direction comes from Mode
// rather than from the background's own brightness: a palette says which
// terminal it was drawn for, so a dark theme with an unusually pale background
// still raises its panels up.
//
// A background of "default" has nothing to lift. The terminal's own color is
// not known here — "default" means "paint nothing and let whatever is behind
// show through" — so the answer is the same default, and a panel drawn on it
// has only its border to separate it. Guessing a shade would be the one thing
// this package refuses to do with a color.
func (t Theme) Surface() Color {
	background := t.Palette.Background
	if background.Default() {
		return background
	}
	target := 0.0
	if t.Mode == Dark {
		target = 255
	}
	lifted := [3]int{}
	for index, channel := range channels(background.hex) {
		lifted[index] = int(math.Round(float64(channel) + (target-float64(channel))*surfaceLift))
	}
	return Color{hex: fmt.Sprintf("#%02x%02x%02x", lifted[0], lifted[1], lifted[2])}
}

// channels reads "#rrggbb" back into three numbers. It cannot fail, and does
// not pretend to: the only string it is ever given has been through
// parseColor, which is what guarantees six lowercase hex digits.
func channels(hex string) [3]int {
	var out [3]int
	for index := range out {
		value, _ := strconv.ParseInt(hex[1+index*2:3+index*2], 16, 0)
		out[index] = int(value)
	}
	return out
}

// document is one theme file exactly as written, before inheritance.
type document struct {
	Version int               `json:"version"`
	Mode    string            `json:"mode"`
	Extends string            `json:"extends"`
	Colors  map[string]string `json:"colors"`
}

// partial is one file after parsing: the parts a chain needs, with colors
// already turned into values.
type partial struct {
	source  string
	mode    Mode
	extends string
	colors  map[Token]Color
}

// parsePartial reads and validates one theme document. source is what the
// error messages name, so it is the path for a file and the theme name for an
// embedded default.
func parsePartial(source string, raw []byte) (partial, error) {
	if len(raw) > maxSize {
		return partial{}, fmt.Errorf("%s: %d bytes exceeds the %d byte limit", source, len(raw), maxSize)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// A theme that misspells a field is a theme the user believes is applied.
	decoder.DisallowUnknownFields()
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return partial{}, fmt.Errorf("%s: %w", source, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return partial{}, fmt.Errorf("%s: trailing data after the theme object", source)
	}

	switch {
	case doc.Version == 0:
		return partial{}, fmt.Errorf("%s: version is required: this build reads version %d", source, Version)
	case doc.Version != Version:
		return partial{}, fmt.Errorf("%s: version %d is not supported by this build, which reads version %d", source, doc.Version, Version)
	}
	mode := Mode(doc.Mode)
	switch mode {
	case Dark, Light:
	default:
		return partial{}, fmt.Errorf("%s: mode %q is neither %q nor %q", source, doc.Mode, Dark, Light)
	}
	if len(doc.Colors) == 0 {
		return partial{}, fmt.Errorf("%s: colors is empty: name at least one token, or use \"extends\"", source)
	}

	// Sorted so the same file always reports the same first problem.
	keys := sortedKeys(doc.Colors)

	colors := make(map[Token]Color, len(keys))
	for _, key := range keys {
		token := Token(key)
		if !knownToken(token) {
			return partial{}, fmt.Errorf("%s: colors.%s is not a token; this build knows: %s", source, key, tokenList(Tokens))
		}
		color, err := parseColor(doc.Colors[key])
		if err != nil {
			return partial{}, fmt.Errorf("%s: colors.%s: %w", source, key, err)
		}
		colors[token] = color
	}

	extends := strings.TrimSpace(doc.Extends)
	if extends == "" && len(colors) != len(Tokens) {
		// Without a base there is nothing to fall back to, so say what is
		// missing now rather than after the merge finds out.
		if missing := missingTokens(colors); len(missing) > 0 {
			return partial{}, missingError(source, missing)
		}
	}
	return partial{source: source, mode: mode, extends: extends, colors: colors}, nil
}

// validateColors checks a set of token overrides: every name has to be a token
// this build knows, and every value a color it can paint.
//
// An override is written in the config file, which cannot reach the palette's
// vocabulary — internal/config depends on nothing — so this is where a
// misspelled token or a named color is caught, when the theme that would carry
// it is resolved and the interface is about to start. The format is parsed by
// the theme's own reader for the obvious reason: two readers of one value are
// how they drift, and the drift shows up as a screen that starts in a color
// nobody asked for.
func validateColors(overrides map[string]string) error {
	for _, key := range sortedKeys(overrides) {
		if !knownToken(Token(key)) {
			return fmt.Errorf("%s is not a token; this build knows: %s", key, tokenList(Tokens))
		}
		if _, err := parseColor(overrides[key]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// Override returns the theme with the named tokens replaced.
//
// The theme stays the whole answer to "what does atlas look like", and an
// override is what the reader wanted different about it. Replacing a token that
// is already there is the only thing it can do, which is why the result is
// still a complete palette rather than a second, partial one.
func (t Theme) Override(overrides map[string]string) (Theme, error) {
	if len(overrides) == 0 {
		return t, nil
	}
	if err := validateColors(overrides); err != nil {
		return Theme{}, err
	}
	colors := t.Palette.colors()
	for _, key := range sortedKeys(overrides) {
		color, err := parseColor(overrides[key])
		if err != nil {
			// validateColors has already parsed it, so this is unreachable:
			// returning rather than ignoring it keeps one reader of the value.
			return Theme{}, fmt.Errorf("%s: %w", key, err)
		}
		colors[Token(key)] = color
	}
	t.Palette, _ = paletteFrom(colors)
	return t, nil
}

// sortedKeys lists a token map's names in a stable order, so the same file
// always reports the same first problem.
func sortedKeys(colors map[string]string) []string {
	keys := make([]string, 0, len(colors))
	for key := range colors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// knownToken reports whether name is a token this build defines.
func knownToken(name Token) bool {
	for _, token := range Tokens {
		if token == name {
			return true
		}
	}
	return false
}

// missingTokens lists what a color map does not define, in the canonical order
// rather than map order, so the message is stable.
func missingTokens(colors map[Token]Color) []Token {
	missing := make([]Token, 0, len(Tokens))
	for _, token := range Tokens {
		if _, ok := colors[token]; !ok {
			missing = append(missing, token)
		}
	}
	return missing
}

// missingError explains an incomplete palette. It names every missing token
// and the alternative, because "colors.border: missing" on its own reads like
// a typo in the file rather than a rule.
func missingError(source string, missing []Token) error {
	return fmt.Errorf("%s: colors is missing %s; a theme must name every token or inherit from one with \"extends\"", source, tokenList(missing))
}

// tokenList renders tokens as a sorted, quoted list for a diagnostic.
func tokenList(tokens []Token) string {
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		parts = append(parts, fmt.Sprintf("%q", string(token)))
	}
	return strings.Join(parts, ", ")
}

// paletteFrom builds the struct from a merged color map. The missing list is
// what a chain that ends without a base leaves behind.
func paletteFrom(colors map[Token]Color) (Palette, []Token) {
	missing := make([]Token, 0, len(Tokens))
	take := func(token Token) Color {
		color, ok := colors[token]
		if !ok {
			missing = append(missing, token)
		}
		return color
	}
	return Palette{
		Text:       take(TokenText),
		Dim:        take(TokenDim),
		Accent:     take(TokenAccent),
		Border:     take(TokenBorder),
		OK:         take(TokenOK),
		Warn:       take(TokenWarn),
		Error:      take(TokenError),
		User:       take(TokenUser),
		Tool:       take(TokenTool),
		Code:       take(TokenCode),
		Selection:  take(TokenSelection),
		Background: take(TokenBackground),
	}, missing
}

// colors turns a resolved palette back into a token map, which is the form the
// merge needs: an override is "the same set, one entry different", and a map is
// the one way to say that without an enumeration per field. It is the inverse
// of paletteFrom and names the same twelve tokens, in the same order.
func (p Palette) colors() map[Token]Color {
	return map[Token]Color{
		TokenText:       p.Text,
		TokenDim:        p.Dim,
		TokenAccent:     p.Accent,
		TokenBorder:     p.Border,
		TokenOK:         p.OK,
		TokenWarn:       p.Warn,
		TokenError:      p.Error,
		TokenUser:       p.User,
		TokenTool:       p.Tool,
		TokenCode:       p.Code,
		TokenSelection:  p.Selection,
		TokenBackground: p.Background,
	}
}

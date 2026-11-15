package theme

import (
	"strings"
	"testing"
)

// everyColor is the twelve token lines a complete theme needs. Spelling it out
// keeps a test document readable as a theme file instead of as a format string.
const everyColor = `"text": "#c0caf5",
    "dim": "#565f89",
    "accent": "#7aa2f7",
    "border": "#3b4261",
    "ok": "#9ece6a",
    "warn": "#e0af68",
    "error": "#f7768e",
    "user": "#7dcfff",
    "tool": "#bb9af7",
    "code": "#73daca",
    "selection": "#2a2f4a",
    "background": "default"`

// themeDoc assembles a document from fields, so a test can leave out exactly
// the thing it is about.
func themeDoc(fields ...string) []byte {
	return []byte("{\n  " + strings.Join(fields, ",\n  ") + "\n}")
}

// completeDoc is a valid theme: the baseline every rejection test starts from.
func completeDoc() []byte {
	return themeDoc(`"version": 1`, `"mode": "dark"`, `"colors": {`+everyColor+`}`)
}

func TestParseColorAcceptsHexAndDefault(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"six digits", "#7aa2f7", "#7aa2f7"},
		{"three digits expand", "#abc", "#aabbcc"},
		{"uppercase is lowered", "#ABCDEF", "#abcdef"},
		{"short uppercase expands", "#ABC", "#aabbcc"},
		{"terminal default", DefaultColor, DefaultColor},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			color, err := parseColor(test.in)
			if err != nil {
				t.Fatalf("parseColor(%q): %v", test.in, err)
			}
			if got := color.String(); got != test.want {
				t.Fatalf("parseColor(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestParseColorMarksTheTerminalDefault(t *testing.T) {
	color, err := parseColor(DefaultColor)
	if err != nil {
		t.Fatalf("parseColor: %v", err)
	}
	if !color.Default() {
		t.Fatalf("parseColor(%q).Default() = false", DefaultColor)
	}
	if color.Hex() != "" {
		t.Fatalf("parseColor(%q).Hex() = %q, want empty", DefaultColor, color.Hex())
	}
	named, err := parseColor("#7aa2f7")
	if err != nil {
		t.Fatalf("parseColor: %v", err)
	}
	if named.Default() {
		t.Fatal("a hex color reports itself as the terminal default")
	}
}

func TestParseColorRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a color name", "red", `"red" is not "default" or a hex color`},
		{"no hash", "7aa2f7", "is not"},
		{"four digits", "#abcd", "4 digits after \"#\""},
		{"five digits", "#abcde", "5 digits after \"#\""},
		{"not hex", "#ggg000", "not a hex digit"},
		{"empty", "", "is not"},
		{"hash only", "#", "0 digits after \"#\""},
		{"uppercase default", "DEFAULT", "is not"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseColor(test.in)
			if err == nil {
				t.Fatalf("parseColor(%q) succeeded", test.in)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseColor(%q) error = %q, want it to contain %q", test.in, err, test.want)
			}
		})
	}
}

func TestParsePartialAcceptsACompleteTheme(t *testing.T) {
	entry, err := parsePartial("dark.json", completeDoc())
	if err != nil {
		t.Fatalf("parsePartial: %v", err)
	}
	if entry.mode != Dark {
		t.Fatalf("mode = %q, want %q", entry.mode, Dark)
	}
	if entry.extends != "" {
		t.Fatalf("extends = %q, want empty", entry.extends)
	}
	if len(entry.colors) != len(Tokens) {
		t.Fatalf("parsed %d colors, want %d", len(entry.colors), len(Tokens))
	}
	for _, token := range Tokens {
		if _, ok := entry.colors[token]; !ok {
			t.Fatalf("token %q was not parsed", token)
		}
	}
}

func TestParsePartialRejectsAMistypedField(t *testing.T) {
	// The field the user meant is "colors"; a theme that carried a top-level
	// "acccent" would otherwise be accepted and change nothing.
	doc := themeDoc(`"version": 1`, `"mode": "dark"`, `"acccent": "#7aa2f7"`, `"colors": {`+everyColor+`}`)
	_, err := parsePartial("dark.json", doc)
	if err == nil {
		t.Fatal("a misspelled field was accepted")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %q, want it to name the unknown field", err)
	}
}

func TestParsePartialRejectsAnUnknownToken(t *testing.T) {
	doc := themeDoc(`"version": 1`, `"mode": "dark"`, `"extends": "base"`, `"colors": {"primary": "#7aa2f7"}`)
	_, err := parsePartial("dark.json", doc)
	if err == nil {
		t.Fatal("an unknown token was accepted")
	}
	if !strings.Contains(err.Error(), "colors.primary is not a token") {
		t.Fatalf("error = %q, want it to name the token", err)
	}
}

func TestParsePartialRequiresVersion(t *testing.T) {
	doc := themeDoc(`"mode": "dark"`, `"colors": {`+everyColor+`}`)
	_, err := parsePartial("dark.json", doc)
	if err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("error = %v, want a missing version", err)
	}
}

func TestParsePartialRejectsAnUnsupportedVersion(t *testing.T) {
	doc := themeDoc(`"version": 2`, `"mode": "dark"`, `"colors": {`+everyColor+`}`)
	_, err := parsePartial("dark.json", doc)
	if err == nil || !strings.Contains(err.Error(), "version 2 is not supported") {
		t.Fatalf("error = %v, want an unsupported version", err)
	}
}

func TestParsePartialRejectsAnUnknownMode(t *testing.T) {
	for _, mode := range []string{"", "dusk", "Dark"} {
		t.Run("mode="+mode, func(t *testing.T) {
			doc := themeDoc(`"version": 1`, `"mode": "`+mode+`"`, `"colors": {`+everyColor+`}`)
			_, err := parsePartial("dark.json", doc)
			if err == nil || !strings.Contains(err.Error(), "is neither") {
				t.Fatalf("error = %v, want a rejected mode", err)
			}
		})
	}
}

func TestParsePartialRejectsEmptyColors(t *testing.T) {
	doc := themeDoc(`"version": 1`, `"mode": "dark"`, `"colors": {}`)
	_, err := parsePartial("dark.json", doc)
	if err == nil || !strings.Contains(err.Error(), "colors is empty") {
		t.Fatalf("error = %v, want an empty colors complaint", err)
	}
}

func TestParsePartialNamesEveryMissingToken(t *testing.T) {
	// No extends, one token: the error has to name the other eleven in the
	// canonical order, because fixing them one restart at a time is the
	// failure mode this rule exists to avoid.
	doc := themeDoc(`"version": 1`, `"mode": "dark"`, `"colors": {"text": "#c0caf5"}`)
	_, err := parsePartial("dark.json", doc)
	if err == nil {
		t.Fatal("an incomplete theme was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, `"dim", "accent", "border", "ok", "warn", "error", "user", "tool", "code", "selection", "background"`) {
		t.Fatalf("error = %q, want every missing token in order", message)
	}
	if strings.Contains(message, `"text"`) {
		t.Fatalf("error = %q names a token that is present", message)
	}
	if !strings.Contains(message, `"extends"`) {
		t.Fatalf("error = %q does not mention the alternative", message)
	}
}

func TestParsePartialAllowsPartialColorsWhenExtending(t *testing.T) {
	doc := themeDoc(`"version": 1`, `"mode": "dark"`, `"extends": "base"`, `"colors": {"accent": "#ff0000"}`)
	entry, err := parsePartial("child.json", doc)
	if err != nil {
		t.Fatalf("parsePartial: %v", err)
	}
	if entry.extends != "base" || len(entry.colors) != 1 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestParsePartialRejectsTrailingData(t *testing.T) {
	doc := append(completeDoc(), []byte("\n{}")...)
	_, err := parsePartial("dark.json", doc)
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("error = %v, want trailing data", err)
	}
}

func TestParsePartialRejectsAnOversizedFile(t *testing.T) {
	_, err := parsePartial("dark.json", make([]byte, maxSize+1))
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %v, want a size complaint", err)
	}
}

// TestAnOverrideReplacesOnlyWhatItNames is the config's colors block: a reader
// changes an accent without maintaining a palette file, and the eleven tokens
// they did not name stay exactly as the theme left them.
func TestAnOverrideReplacesOnlyWhatItNames(t *testing.T) {
	library, err := NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	base, err := library.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	overridden, err := base.Override(map[string]string{"accent": "#FF0000", "dim": DefaultColor})
	if err != nil {
		t.Fatalf("Override: %v", err)
	}
	// #FF0000 is the override said loudly, and it comes back the way a theme
	// writes it: lowercase, six digits.
	if got := overridden.Palette.Accent.Hex(); got != "#ff0000" {
		t.Fatalf("accent = %q, want the override", got)
	}
	if !overridden.Palette.Dim.Default() {
		t.Fatalf("dim = %q, want the terminal default", overridden.Palette.Dim)
	}
	before, after := base.Palette.colors(), overridden.Palette.colors()
	for _, token := range Tokens {
		if token == TokenAccent || token == TokenDim {
			continue
		}
		if before[token].String() != after[token].String() {
			t.Fatalf("%s moved to %q, want %q", token, after[token], before[token])
		}
	}
	// The name and the mode are the file's, not the override's: /theme still
	// has to report which palette is in force.
	if overridden.Name != base.Name || overridden.Mode != base.Mode {
		t.Fatalf("the override renamed the theme: %+v", overridden)
	}
}

// TestAnOverrideIsCheckedLikeATheme keeps one reader of the color format: a
// value the theme parser refuses is refused here with the same words, and a
// token the theme does not know is named rather than ignored.
func TestAnOverrideIsCheckedLikeATheme(t *testing.T) {
	base := Theme{Name: "atlas-dark", Mode: Dark}
	cases := []struct {
		name   string
		token  string
		value  string
		expect string
	}{
		{"unknown token", "acccent", "#ff0000", "is not a token"},
		{"named color", "accent", "blue", "not \"default\""},
		{"two digits", "accent", "#12", "3 or 6"},
		{"not a hex digit", "accent", "#12345g", "hex digit"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := base.Override(map[string]string{testCase.token: testCase.value})
			if err == nil {
				t.Fatalf("Override accepted %s = %q", testCase.token, testCase.value)
			}
			if !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("error = %v, want %q", err, testCase.expect)
			}
		})
	}
}

// An empty override is the ordinary config, and it has to cost nothing: a
// a validateColors that rejected an empty map would refuse every file that does
// not tune the screen.
func TestAnEmptyOverrideIsTheThemeItself(t *testing.T) {
	if err := validateColors(nil); err != nil {
		t.Fatalf("validateColors(nil): %v", err)
	}
	theme := Theme{Name: "atlas-dark", Mode: Dark}
	got, err := theme.Override(nil)
	if err != nil {
		t.Fatalf("Override: %v", err)
	}
	if got.Name != theme.Name || got.Mode != theme.Mode {
		t.Fatalf("Override(nil) = %+v, want the theme back", got)
	}
}

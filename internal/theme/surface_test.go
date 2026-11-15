package theme

import "testing"

// surface builds a theme whose only interesting field is the screen color.
func surface(t *testing.T, mode Mode, background string) Color {
	t.Helper()
	color, err := parseColor(background)
	if err != nil {
		t.Fatalf("parseColor(%q): %v", background, err)
	}
	return Theme{Name: "test", Mode: mode, Palette: Palette{Background: color}}.Surface()
}

// TestSurfaceLiftsAwayFromTheScreen pins the two things a derived shade can get
// wrong: the direction, and the step.
//
// The direction is a property of the palette's mode rather than of the number
// in the background: a "raised" panel that came out darker than the screen
// would be sunken, and the reader would see a hole in the interface instead of
// a card on top of it. The step is pinned because it is what makes the panel
// visible at all — a number that rounds to the same hex is a panel nobody can
// see, and that is exactly the failure a "derive it" design invites.
func TestSurfaceLiftsAwayFromTheScreen(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		mode       Mode
		background string
		want       string
	}{
		// The built-in dark palette, and the surface the composer's box draws
		// with: eighteen levels per channel, which a terminal shows plainly.
		{"dark screen", Dark, "#1a1b26", "#2c2d37"},
		{"light screen", Light, "#e1e2e7", "#cfd0d5"},
		// The ends of the range cannot move further out, and must not wrap
		// around into the opposite direction.
		{"black on a dark palette", Dark, "#000000", "#141414"},
		{"white on a dark palette", Dark, "#ffffff", "#ffffff"},
		{"black on a light palette", Light, "#000000", "#000000"},
		{"white on a light palette", Light, "#ffffff", "#ebebeb"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := surface(t, testCase.mode, testCase.background)
			if got.Hex() != testCase.want {
				t.Fatalf("Surface() = %q, want %q", got.Hex(), testCase.want)
			}
			// A background already at the end it would move toward has no room
			// to move; everywhere else staying put would be a panel nobody can
			// see.
			if testCase.want != testCase.background && got.Hex() == testCase.background {
				t.Fatalf("Surface() kept the screen color %q: the panel would be invisible", testCase.background)
			}
		})
	}
}

// TestSurfaceOfATerminalBackgroundIsStillDefault: "default" means "let the
// terminal paint that cell", and there is no number here to lift. Guessing a
// shade would paint over a background this package has promised not to touch,
// so the panel gets no fill and its border has to carry the separation.
func TestSurfaceOfATerminalBackgroundIsStillDefault(t *testing.T) {
	for _, mode := range []Mode{Dark, Light} {
		got := surface(t, mode, DefaultColor)
		if !got.Default() {
			t.Fatalf("%s: Surface() = %q, want the terminal default", mode, got.Hex())
		}
	}
}

// TestTheBuiltInPalettesPaintTheScreen: the surface is derived from the screen
// color, so a shipped palette that leaves the background to the terminal would
// ship the whole feature switched off. Both defaults have to name one.
func TestTheBuiltInPalettesPaintTheScreen(t *testing.T) {
	for _, row := range []struct{ name, want string }{
		{"atlas-dark", "#1a1b26"},
		{"atlas-light", "#e1e2e7"},
	} {
		library, err := NewLibrary(nil, false)
		if err != nil {
			t.Fatalf("NewLibrary: %v", err)
		}
		resolved, err := library.Resolve(row.name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", row.name, err)
		}
		if got := resolved.Palette.Background.Hex(); got != row.want {
			t.Fatalf("%s: background = %q, want %q", row.name, got, row.want)
		}
	}
}

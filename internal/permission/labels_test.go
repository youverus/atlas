package permission

import "testing"

// TestLabelsNameAllFourModes guards the table the terminal and the window both
// read. A missing row would draw a menu with a blank name, and a blank label is
// worse than a wrong one: it reads as a posture nobody described.
func TestLabelsNameAllFourModes(t *testing.T) {
	for _, mode := range []string{ModeDefault, ModeAuto, ModeBypass, ModeOff} {
		if Label(mode) == "" {
			t.Fatalf("Label(%q) is empty", mode)
		}
		if Label(mode) == mode {
			t.Fatalf("Label(%q) fell through to the raw value; the row has no reader-facing name", mode)
		}
		if Note(mode) == "" {
			t.Fatalf("Note(%q) is empty", mode)
		}
	}
}

// TestLabelKeepsAnUnknownMode says a mode this build does not know is a fact
// about the session, not something to hide behind a placeholder: the footer and
// the settings sheet both print what they were given.
func TestLabelKeepsAnUnknownMode(t *testing.T) {
	if got := Label("paranoid"); got != "paranoid" {
		t.Fatalf("Label(%q) = %q, want the value back", "paranoid", got)
	}
	if got := Note("paranoid"); got != "" {
		t.Fatalf("Note(%q) = %q, want empty", "paranoid", got)
	}
}

// TestLabelsAreDistinct is the reason the two surfaces share one table: two
// modes with one name would make the reader's choice ambiguous.
func TestLabelsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, mode := range []string{ModeDefault, ModeAuto, ModeBypass, ModeOff} {
		label := Label(mode)
		if other, ok := seen[label]; ok {
			t.Fatalf("%q and %q share the label %q", other, mode, label)
		}
		seen[label] = mode
	}
}

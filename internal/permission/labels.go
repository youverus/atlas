package permission

// The words a reader sees for the four postures.
//
// They live here, beside the modes, because two interfaces now show them: the
// terminal's /permission menu and the window's settings sheet. A copy in each
// front end would drift, and the drift would be a reader comparing the two and
// finding two names for one posture.
//
// The labels are the reference implementation's own — Ask, Auto, Full access,
// Off — so a user who read its documentation recognises the row without
// translating. The values underneath stay the config spelling, which is why the
// two most misleading values are the two that get a different word: "default"
// says nothing about what happens, and "bypassPermissions" is a sentence.
//
// This package stays a leaf: the table is data, not a dependency on whoever
// draws it.
var labels = map[string]struct{ label, note string }{
	ModeDefault: {"Ask", "ask before every change"},
	ModeAuto:    {"Auto", "ask the classifier, and the user when it cannot answer"},
	ModeBypass:  {"Full access", "no fallback ask; deny and ask rules still run"},
	ModeOff:     {"Off", "no rules, no checks, no ask"},
}

// Label is the short name for mode, for a footer or a chip.
//
// An unknown value is returned as it is: a mode this build does not know is a
// fact about the session, not something to hide behind "unknown".
func Label(mode string) string {
	if named, ok := labels[mode]; ok {
		return named.label
	}
	return mode
}

// Note is the one line that says what mode actually does, for a menu row.
func Note(mode string) string {
	return labels[mode].note
}

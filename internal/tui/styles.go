package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"pi-go/internal/theme"
)

// Styles is the resolved look of the interface: one lipgloss style per
// semantic token. Every draw call above this type names a slot instead of a
// color, so a palette change never reaches the code that builds a frame.
//
// The mapping from token to slot is one to one on purpose. A theme author who
// writes "accent" should be able to predict which pixels move, and a second
// layer of meaning ("accent but only in the composer") would make that
// impossible to answer without reading the interface.
type Styles struct {
	Text       lipgloss.Style
	Dim        lipgloss.Style
	Accent     lipgloss.Style
	Border     lipgloss.Style
	OK         lipgloss.Style
	Warn       lipgloss.Style
	Error      lipgloss.Style
	User       lipgloss.Style
	Tool       lipgloss.Style
	Code       lipgloss.Style
	Selection  lipgloss.Style
	Background lipgloss.Style

	// palette is the theme's colors, kept because every slot above is a
	// finished answer for one background and the raised panel needs the same
	// answer for another. Rebuilding from the palette is the only way to get
	// it without naming the colors twice.
	palette theme.Palette

	// raised is the background of a panel that sits above the screen. It comes
	// from the theme rather than from a token: see theme.Theme.Surface.
	raised theme.Color
}

// NewStyles builds the styles for one theme.
func NewStyles(t theme.Theme) Styles {
	raised := t.Surface()
	styles := build(t.Palette, t.Palette.Background)
	styles.palette, styles.raised = t.Palette, raised
	return styles
}

// Panel returns the same look on the raised background: what everything inside
// the composer's box is drawn with.
//
// A cell's background comes from what was written into it, so a panel's text
// cannot simply be drawn over the panel: every inline style ends with a reset,
// and the screen's color would come back through the letters. Rebuilding the
// slots on the surface color is what makes the box one surface with text on
// it rather than text with holes in it.
func (s Styles) Panel() Styles {
	panel := build(s.palette, s.raised)
	panel.palette, panel.raised = s.palette, s.raised
	return panel
}

// Surface reports the background a panel is painted with, which is the screen's
// own color when the theme leaves that to the terminal.
func (s Styles) Surface() theme.Color { return s.raised }

// build composes every slot on top of bg: the screen for the frame, the raised
// surface for a panel. Composing rather than adding a background to each style
// where it is used is what keeps a palette one table.
func build(p theme.Palette, bg theme.Color) Styles {
	base := lipgloss.NewStyle().Background(paint(bg))
	foreground := func(c theme.Color) lipgloss.Style { return base.Foreground(paint(c)) }
	return Styles{
		Text:       foreground(p.Text),
		Dim:        foreground(p.Dim),
		Accent:     foreground(p.Accent),
		Border:     foreground(p.Border),
		OK:         foreground(p.OK),
		Warn:       foreground(p.Warn),
		Error:      foreground(p.Error),
		User:       foreground(p.User),
		Tool:       foreground(p.Tool),
		Code:       foreground(p.Code),
		Selection:  base.Background(paint(p.Selection)),
		Background: background(bg),
	}
}

// foreground is the whole of the theme's contract with lipgloss: a hex string
// or "default", nothing else. Converting one into the other here rather than
// in internal/theme is what keeps that package on the standard library.
func foreground(c theme.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(paint(c))
}

func background(c theme.Color) lipgloss.Style {
	return lipgloss.NewStyle().Background(paint(c))
}

// paint maps a theme color to what lipgloss accepts. "default" becomes
// NoColor rather than a guess: the point of the value is that the terminal's
// own foreground or background shows through.
//
// An unset color is the same answer, which matters because a Styles built
// without a theme — the zero value tests use — has every slot unset. Handing
// lipgloss an empty string instead would be asking it to parse a color that
// spells nothing.
func paint(c theme.Color) color.Color {
	if c.Default() || c.Hex() == "" {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color(c.Hex())
}

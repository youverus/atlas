package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// loadTUI reads the interface file a config names.
//
// It is a second read rather than a second decode into the same struct because
// the two files fail for different reasons and the reader needs to know which
// one they are editing: every error here names this file, with its path, and
// never the file that pointed at it. The size limit and the "one value only"
// check are the same as the config's, for the same reasons — a hand-written
// appearance file is just as easy to paste twice.
func loadTUI(name, configPath string) (UI, error) {
	path, err := resolveTUIPath(name, configPath)
	if err != nil {
		return UI{}, fmt.Errorf("config: tui: %w", err)
	}
	handle, err := os.Open(path)
	if err != nil {
		return UI{}, fmt.Errorf("config: tui: %w", err)
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		return UI{}, fmt.Errorf("config: tui: %w", err)
	}
	if info.Size() > maxSize {
		return UI{}, fmt.Errorf("config: tui: %s: %d bytes exceeds the %d byte limit", path, info.Size(), maxSize)
	}

	decoder := json.NewDecoder(io.LimitReader(handle, maxSize))
	decoder.DisallowUnknownFields()
	var tui UI
	if err := decoder.Decode(&tui); err != nil {
		return UI{}, fmt.Errorf("config: tui: %s: %w", path, movedLayout(err))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return UI{}, fmt.Errorf("config: tui: %s: trailing data after the object", path)
	}
	if err := tui.validate(); err != nil {
		return UI{}, fmt.Errorf("config: tui: %s: %w", path, err)
	}
	return tui, nil
}

// movedLayout answers the question a reader upgrading from 0.4.12 asks: the
// geometry keys used to be flat, and "unknown field" alone does not say where
// one went.
//
// The list is written out rather than derived from a prefix rule because the
// names changed as well as moved — "composerGap" is now the composer's
// "gapAbove" — so no pattern would let a reader guess the new spelling.
func movedLayout(err error) error {
	text := err.Error()
	// The 0.4.11 and 0.4.12 files spell the whole geometry as one "layout"
	// object, so they fail on that key before any number inside it is looked
	// at — which is why this case cannot be folded into the table below.
	if strings.Contains(text, `unknown field "layout"`) {
		return fmt.Errorf(`%w (the geometry is grouped under "blocks", and "roles" for the blocks inside the transcript)`, err)
	}
	for _, move := range layoutMoves {
		if strings.Contains(text, `unknown field "`+move.was+`"`) {
			return fmt.Errorf("%w (that key is now %s)", err, move.now)
		}
	}
	return err
}

// layoutMoves is the 0.4.12 flat geometry, in the order the file listed it,
// and the path each number is written at now.
var layoutMoves = []struct{ was, now string }{
	{"blockGap", "blocks.transcript.gap"},
	{"composerGap", "blocks.composer.gapAbove"},
	{"composerRows", "blocks.composer.rows"},
	{"paletteRows", "blocks.palette.rows"},
	{"statusGap", "blocks.status.gapAbove"},
	{"menuRows", "blocks.menu.rows"},
	{"menuWidth", "blocks.menu.width"},
	{"boxWidth", "blocks.approval.width"},
	{"thinkingRows", "roles.thinking.rows"},
	{"homePage", "blocks.welcome.on"},
}

// resolveTUIPath answers where "tui" points.
//
// A relative name is relative to the file that named it, not to the working
// directory: the two are the same only when atlas happens to be started from
// the config's own directory, and "it works in one terminal" is the worst kind
// of working. "~" is expanded like every other path in this package.
func resolveTUIPath(name, configPath string) (string, error) {
	expanded, err := ExpandHome(strings.TrimSpace(name))
	if err != nil {
		return "", err
	}
	if expanded == "" {
		return "", errors.New(`"tui" is empty: name a file, or leave the key out to use the built-in appearance`)
	}
	if filepath.IsAbs(expanded) {
		return expanded, nil
	}
	return filepath.Join(filepath.Dir(configPath), expanded), nil
}

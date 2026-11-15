package theme

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// defaults holds the palettes that ship inside the binary, so a fresh install
// has something to draw with before anyone writes a theme file.
//
//go:embed defaults/*.json
var defaults embed.FS

// defaultsDir is where the embedded palettes sit inside the binary.
const defaultsDir = "defaults"

// FileExt is the extension a theme file must have to be discovered.
const FileExt = ".json"

// Auto asks for a theme chosen by the terminal's background instead of by name.
const Auto = "auto"

// defaultDark and defaultLight are the embedded palettes Auto picks between.
const (
	defaultDark  = "atlas-dark"
	defaultLight = "atlas-light"
)

// BrokenTheme is a file that was found and could not be used.
type BrokenTheme struct {
	// Name is the theme name the file would have had.
	Name string
	// Err is why it will not parse, already prefixed with the file it came
	// from.
	Err error
}

// Library is the set of themes a run can see: the embedded defaults, plus
// whatever the directories it was given add.
//
// Loading is eager and resolution is lazy. Every file is parsed once at
// startup, because that is cheap and because a file that does not parse has to
// be reported even when nobody asks for it; the inheritance chain is walked
// only when a theme is actually used, so a broken base cannot fail a run that
// never touches it.
type Library struct {
	entries map[string]partial
	broken  map[string]error
	dark    bool
}

// NewLibrary collects every theme in dirs. Later directories win, so the
// caller passes them farthest first and a project can override a global theme
// by giving the file the same name. dark says which embedded palette Auto
// means.
//
// A directory that does not exist is skipped: having no themes of your own is
// the normal case. A file that does not parse is recorded, not fatal: it is
// the user's file, and refusing to start over it would take the whole
// interface down for a typo it can describe.
func NewLibrary(dirs []string, dark bool) (*Library, error) {
	library := &Library{
		entries: make(map[string]partial),
		broken:  make(map[string]error),
		dark:    dark,
	}
	if err := library.loadDefaults(); err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if err := library.loadDir(dir); err != nil {
			return nil, err
		}
	}
	return library, nil
}

// loadDefaults installs the embedded palettes. A failure here is a broken
// build rather than a broken configuration, so it is returned instead of
// recorded.
func (l *Library) loadDefaults() error {
	names, err := fs.Glob(defaults, defaultsDir+"/*"+FileExt)
	if err != nil {
		return fmt.Errorf("theme: embedded palettes: %w", err)
	}
	if len(names) == 0 {
		return errors.New("theme: the build embeds no palettes")
	}
	sort.Strings(names)
	for _, path := range names {
		raw, err := defaults.ReadFile(path)
		if err != nil {
			return fmt.Errorf("theme: embedded %s: %w", path, err)
		}
		name := strings.TrimSuffix(filepath.Base(path), FileExt)
		entry, err := parsePartial(name, raw)
		if err != nil {
			return fmt.Errorf("theme: embedded %s: %w", path, err)
		}
		l.entries[name] = entry
	}
	return nil
}

// loadDir adds one directory. Missing directories are not an error; anything
// else about the directory is.
func (l *Library) loadDir(dir string) error {
	items, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("theme: %s: %w", dir, err)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), FileExt) {
			continue
		}
		names = append(names, item.Name())
	}
	// Sorted so a directory full of broken files reports its problems in the
	// same order every run.
	sort.Strings(names)

	for _, file := range names {
		path := filepath.Join(dir, file)
		name := strings.TrimSuffix(file, FileExt)
		raw, err := readBounded(path)
		if err != nil {
			l.shadow(name, fmt.Errorf("%s: %w", path, err))
			continue
		}
		entry, err := parsePartial(path, raw)
		if err != nil {
			l.shadow(name, err)
			continue
		}
		// A file that parses replaces the same name from a farther directory,
		// and stops being broken if it was.
		delete(l.broken, name)
		l.entries[name] = entry
	}
	return nil
}

// shadow makes name unusable and remembers why. It removes the entry on
// purpose: falling back to a working theme of the same name would apply
// something other than the file the user wrote, which is the one outcome worth
// failing over.
func (l *Library) shadow(name string, err error) {
	delete(l.entries, name)
	l.broken[name] = err
}

// readBounded reads at most one theme file, refusing an oversized one before
// it is in memory.
func readBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("is a directory")
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%d bytes exceeds the %d byte limit", info.Size(), maxSize)
	}
	return os.ReadFile(path)
}

// Names lists the themes that can be used, sorted.
func (l *Library) Names() []string {
	names := make([]string, 0, len(l.entries))
	for name := range l.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Broken lists the files that were found and rejected, sorted by name. The
// interface shows them: a theme file the user wrote and atlas ignores should
// be louder than one it does not have.
func (l *Library) Broken() []BrokenTheme {
	names := make([]string, 0, len(l.broken))
	for name := range l.broken {
		names = append(names, name)
	}
	sort.Strings(names)
	broken := make([]BrokenTheme, 0, len(names))
	for _, name := range names {
		broken = append(broken, BrokenTheme{Name: name, Err: l.broken[name]})
	}
	return broken
}

// Resolve turns a query into a theme. An empty query and Auto are the same
// request: pick the embedded palette that matches the terminal background the
// caller reported.
func (l *Library) Resolve(query string) (Theme, error) {
	query = strings.TrimSpace(query)
	if query == "" || query == Auto {
		if l.dark {
			query = defaultDark
		} else {
			query = defaultLight
		}
	}
	return l.resolve(query)
}

// resolve walks the inheritance chain of one named theme and merges it.
func (l *Library) resolve(name string) (Theme, error) {
	if err, ok := l.broken[name]; ok {
		return Theme{}, err
	}
	nodes, err := l.chain(name)
	if err != nil {
		return Theme{}, err
	}
	colors := make(map[Token]Color, len(Tokens))
	// Root first, so a child overwrites its base token by token.
	for _, node := range nodes {
		for token, color := range node.colors {
			colors[token] = color
		}
	}
	// The last node is the theme itself: its mode is the one that applies.
	self := nodes[len(nodes)-1]
	palette, missing := paletteFrom(colors)
	if len(missing) > 0 {
		// parsePartial only accepts a theme that either names every token or
		// inherits from one that does, so a complete chain cannot land here.
		// Saying "bug" rather than "missing token" keeps the two apart: nothing
		// the user can edit produces this.
		return Theme{}, fmt.Errorf("%s: internal: %s missing after merging the extends chain", self.source, tokenList(missing))
	}
	return Theme{Name: name, Mode: self.mode, Palette: palette}, nil
}

// chain returns the extends chain root first, so merging is one pass.
func (l *Library) chain(name string) ([]partial, error) {
	nodes := make([]partial, 0, maxExtends)
	seen := make(map[string]bool, maxExtends)

	current := name
	for depth := 0; ; depth++ {
		if depth >= maxExtends {
			return nil, fmt.Errorf("%s: extends chain is longer than %d themes", name, maxExtends)
		}
		if err, ok := l.broken[current]; ok {
			return nil, err
		}
		node, ok := l.entries[current]
		if !ok {
			if len(nodes) == 0 {
				return nil, fmt.Errorf("theme %q is not defined; this build has: %s", name, strings.Join(l.Names(), ", "))
			}
			return nil, fmt.Errorf("%s: extends %q, which is not defined; available: %s", nodes[len(nodes)-1].source, current, strings.Join(l.Names(), ", "))
		}
		if seen[current] {
			return nil, fmt.Errorf("%s: extends chain loops back to %q", name, current)
		}
		seen[current] = true
		nodes = append(nodes, node)
		if node.extends == "" {
			break
		}
		current = node.extends
	}

	for left, right := 0, len(nodes)-1; left < right; left, right = left+1, right-1 {
		nodes[left], nodes[right] = nodes[right], nodes[left]
	}
	return nodes, nil
}

// Dirs returns the directories a run reads themes from, farthest first: the
// one under the atlas home, then the one in the working directory. A project
// therefore overrides a global theme by giving it the same name, and adding a
// directory later is a change here instead of in a caller.
func Dirs(home, workdir string) []string {
	return []string{
		filepath.Join(home, "themes"),
		filepath.Join(workdir, ".atlas", "themes"),
	}
}

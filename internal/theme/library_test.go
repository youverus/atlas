package theme

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// newDir creates a scratch theme directory inside a test's temporary tree.
func newDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// baseDoc is a complete theme under a name a child can extend.
func baseDoc(mode, accent string) string {
	return string(themeDoc(
		`"version": 1`,
		`"mode": "`+mode+`"`,
		`"colors": {`+strings.ReplaceAll(everyColor, `"accent": "#7aa2f7"`, `"accent": "`+accent+`"`)+`}`,
	))
}

func TestLibraryEmbedsTwoCompletePalettes(t *testing.T) {
	library, err := NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	names := library.Names()
	if len(names) != 2 || names[0] != "atlas-dark" || names[1] != "atlas-light" {
		t.Fatalf("Names = %v, want the two embedded palettes", names)
	}
	for _, name := range names {
		resolved, err := library.Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", name, err)
		}
		// Completeness is the property an embedded palette must have: a zero
		// Color here would draw nothing at all.
		for _, color := range []Color{
			resolved.Palette.Text, resolved.Palette.Dim, resolved.Palette.Accent, resolved.Palette.Border,
			resolved.Palette.OK, resolved.Palette.Warn, resolved.Palette.Error,
			resolved.Palette.User, resolved.Palette.Tool, resolved.Palette.Code,
			resolved.Palette.Selection, resolved.Palette.Background,
		} {
			if color.String() == "" {
				t.Fatalf("%s resolved a token with no value", name)
			}
		}
	}
	if len(library.Broken()) != 0 {
		t.Fatalf("Broken = %v, want none", library.Broken())
	}
}

func TestLibraryAutoPicksByBackground(t *testing.T) {
	dark, err := NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	for _, query := range []string{"", "auto", " auto "} {
		resolved, err := dark.Resolve(query)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", query, err)
		}
		if resolved.Name != "atlas-dark" || resolved.Mode != Dark {
			t.Fatalf("Resolve(%q) = %s/%s, want atlas-dark/dark", query, resolved.Name, resolved.Mode)
		}
	}
	light, err := NewLibrary(nil, false)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := light.Resolve("auto")
	if err != nil {
		t.Fatalf("Resolve(auto): %v", err)
	}
	if resolved.Name != "atlas-light" || resolved.Mode != Light {
		t.Fatalf("Resolve(auto) = %s/%s, want atlas-light/light", resolved.Name, resolved.Mode)
	}
}

func TestLibraryNearestDirectoryWins(t *testing.T) {
	root := t.TempDir()
	global := newDir(t, root, "global")
	project := newDir(t, root, "project")
	write(t, global, "mine.json", baseDoc("dark", "#111111"))
	write(t, project, "mine.json", baseDoc("dark", "#222222"))

	// Farthest first, which is the order Dirs returns.
	library, err := NewLibrary([]string{global, project}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("mine")
	if err != nil {
		t.Fatalf("Resolve(mine): %v", err)
	}
	if got := resolved.Palette.Accent.Hex(); got != "#222222" {
		t.Fatalf("accent = %q, want the project file to win", got)
	}
}

func TestLibrarySkipsMissingDirectories(t *testing.T) {
	library, err := NewLibrary([]string{filepath.Join(t.TempDir(), "absent")}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	if len(library.Names()) != 2 {
		t.Fatalf("Names = %v, want the embedded palettes only", library.Names())
	}
}

func TestLibraryIgnoresEverythingButJSON(t *testing.T) {
	dir := newDir(t, t.TempDir(), "themes")
	write(t, dir, "notes.txt", "not a theme")
	write(t, dir, "README", "not a theme either")
	if err := os.MkdirAll(filepath.Join(dir, "nested.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	if names := library.Names(); len(names) != 2 {
		t.Fatalf("Names = %v, want only the embedded palettes", names)
	}
	if broken := library.Broken(); len(broken) != 0 {
		t.Fatalf("Broken = %v, want none", broken)
	}
}

func TestLibraryReportsABrokenFileWithoutFailing(t *testing.T) {
	dir := newDir(t, t.TempDir(), "themes")
	path := write(t, dir, "half.json", `{"version": 1, "mode": "dark", "colors": {}}`)

	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	broken := library.Broken()
	if len(broken) != 1 || broken[0].Name != "half" {
		t.Fatalf("Broken = %v, want half", broken)
	}
	if !strings.Contains(broken[0].Err.Error(), path) {
		t.Fatalf("error = %q, want it to name %s", broken[0].Err, path)
	}
	if _, err := library.Resolve("half"); err == nil {
		t.Fatal("Resolve accepted a broken theme")
	}
}

func TestLibraryBrokenFileShadowsTheSameName(t *testing.T) {
	// A project file named atlas-dark.json that does not parse must not let the
	// embedded palette answer for it: the user would be looking at something
	// other than the file they wrote.
	dir := newDir(t, t.TempDir(), "themes")
	write(t, dir, "atlas-dark.json", `{"version": 1, "mode": "dark", "colors": {}}`)

	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	if _, err := library.Resolve("atlas-dark"); err == nil {
		t.Fatal("the embedded palette answered for a broken override")
	}
	if _, err := library.Resolve("auto"); err == nil {
		t.Fatal("auto resolved to a broken override")
	}
	if _, err := library.Resolve("atlas-light"); err != nil {
		t.Fatalf("Resolve(atlas-light): %v", err)
	}
}

func TestLibraryExtendsInheritsAndOverrides(t *testing.T) {
	dir := newDir(t, t.TempDir(), "themes")
	write(t, dir, "base.json", baseDoc("dark", "#111111"))
	write(t, dir, "child.json", string(themeDoc(
		`"version": 1`,
		`"mode": "light"`,
		`"extends": "base"`,
		`"colors": {"accent": "#ff0000", "background": "#000000"}`,
	)))

	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	resolved, err := library.Resolve("child")
	if err != nil {
		t.Fatalf("Resolve(child): %v", err)
	}
	if got := resolved.Palette.Accent.Hex(); got != "#ff0000" {
		t.Fatalf("accent = %q, want the child's value", got)
	}
	if got := resolved.Palette.Text.Hex(); got != "#c0caf5" {
		t.Fatalf("text = %q, want the base's value", got)
	}
	if resolved.Palette.Background.Default() {
		t.Fatalf("background = %q, want the child's override to win", resolved.Palette.Background)
	}
	if got := resolved.Palette.Background.Hex(); got != "#000000" {
		t.Fatalf("background = %q, want %q", got, "#000000")
	}
	// The mode is the theme's own, not the base's: the child says which
	// background it was drawn for.
	if resolved.Mode != Light {
		t.Fatalf("mode = %q, want %q", resolved.Mode, Light)
	}
}

func TestLibraryDetectsAnExtendsLoop(t *testing.T) {
	dir := newDir(t, t.TempDir(), "themes")
	write(t, dir, "a.json", `{"version": 1, "mode": "dark", "extends": "b", "colors": {"text": "#c0caf5"}}`)
	write(t, dir, "b.json", `{"version": 1, "mode": "dark", "extends": "a", "colors": {"dim": "#565f89"}}`)

	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	_, err = library.Resolve("a")
	if err == nil || !strings.Contains(err.Error(), `loops back to "a"`) {
		t.Fatalf("error = %v, want a loop", err)
	}
}

func TestLibraryDetectsAnExtendsChainThatIsTooLong(t *testing.T) {
	dir := newDir(t, t.TempDir(), "themes")
	// One more link than the limit allows, each naming the next.
	total := maxExtends + 1
	for index := 0; index < total; index++ {
		name := fmt.Sprintf("t%02d", index)
		next := ""
		if index+1 < total {
			next = fmt.Sprintf(`, "extends": "t%02d"`, index+1)
		}
		write(t, dir, name+".json", fmt.Sprintf(`{"version": 1, "mode": "dark"%s, "colors": {"text": "#c0caf5"}}`, next))
	}
	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	_, err = library.Resolve("t00")
	if err == nil || !strings.Contains(err.Error(), "extends chain is longer") {
		t.Fatalf("error = %v, want a depth complaint", err)
	}
}

func TestLibraryRejectsAnIncompleteBase(t *testing.T) {
	// The end of an extends chain has nothing to inherit from, so it has to be
	// complete on its own. Catching it at load means the error names the file
	// that is short a token, not the child that inherited the gap.
	dir := newDir(t, t.TempDir(), "themes")
	write(t, dir, "child.json", `{"version": 1, "mode": "dark", "extends": "base", "colors": {"accent": "#ff0000"}}`)
	base := write(t, dir, "base.json", `{"version": 1, "mode": "dark", "colors": {"text": "#c0caf5"}}`)

	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	if _, err := library.Resolve("child"); err == nil {
		t.Fatal("Resolve accepted a child of an incomplete base")
	}
	broken := library.Broken()
	if len(broken) != 1 || broken[0].Name != "base" {
		t.Fatalf("Broken = %v, want base", broken)
	}
	if !strings.Contains(broken[0].Err.Error(), base) {
		t.Fatalf("error = %q, want it to name %s", broken[0].Err, base)
	}
}

func TestLibraryResolveRejectsAnUnknownName(t *testing.T) {
	library, err := NewLibrary(nil, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	_, err = library.Resolve("solarized")
	if err == nil {
		t.Fatal("Resolve accepted an unknown name")
	}
	message := err.Error()
	if !strings.Contains(message, `"solarized" is not defined`) || !strings.Contains(message, "atlas-dark, atlas-light") {
		t.Fatalf("error = %q, want the name and the available themes", message)
	}
}

func TestLibraryBrokenIsSorted(t *testing.T) {
	dir := newDir(t, t.TempDir(), "themes")
	for _, name := range []string{"zulu", "alpha", "mike"} {
		write(t, dir, name+".json", `{"version": 1}`)
	}
	library, err := NewLibrary([]string{dir}, true)
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	broken := library.Broken()
	if len(broken) != 3 {
		t.Fatalf("Broken = %v, want three", broken)
	}
	for index, want := range []string{"alpha", "mike", "zulu"} {
		if broken[index].Name != want {
			t.Fatalf("Broken[%d] = %s, want %s", index, broken[index].Name, want)
		}
	}
}

func TestDirsPutsTheProjectLast(t *testing.T) {
	dirs := Dirs("/home/.atlas", "/work")
	want := []string{"/home/.atlas/themes", "/work/.atlas/themes"}
	if len(dirs) != len(want) {
		t.Fatalf("Dirs = %v, want %v", dirs, want)
	}
	for index := range want {
		if dirs[index] != want[index] {
			t.Fatalf("Dirs = %v, want %v", dirs, want)
		}
	}
}

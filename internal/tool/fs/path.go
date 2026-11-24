package fs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// resolveToCwd turns a model-supplied path into an absolute one. Models write
// paths the way a person would: sometimes with a leading "@" copied from editor
// UI, sometimes with a "~", often relative. All three are handled here so every
// tool sees one canonical form.
func resolveToCwd(path, cwd string) string {
	path = normalizeUnicodeSpaces(path)
	if home, err := os.UserHomeDir(); err == nil {
		var expanded bool
		path, expanded = expandTilde(path, home)
		if expanded {
			return filepath.Clean(path)
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd == "" {
		cwd = "."
	}
	return filepath.Clean(filepath.Join(cwd, path))
}

// resolveAccess returns the path used by the read-only filesystem operations.
func (s *shared) resolveAccess(name string) (string, error) {
	if s.root == nil {
		return s.resolve(name), nil
	}
	name = filepath.FromSlash(name)
	if home, err := os.UserHomeDir(); err == nil {
		name, _ = expandTilde(name, filepath.FromSlash(home))
	}
	if filepath.VolumeName(name) == "" && (name == "" || !os.IsPathSeparator(name[0])) {
		return normalizeUnicodeSpaces(name), nil
	}

	// Use the absolute root name only to translate absolute arguments. The
	// handle resolves the remaining components, including symlinks and "..".
	base := filepath.Clean(s.root.Name())
	if filepath.IsAbs(base) {
		if name == base {
			return ".", nil
		}
		if relative, ok := strings.CutPrefix(name, joinAccessPath(base, "")); ok {
			relative = strings.TrimLeft(relative, string(filepath.Separator))
			if relative == "" {
				return ".", nil
			}
			return normalizeUnicodeSpaces(relative), nil
		}
	}
	return "", &fs.PathError{Op: "resolve", Path: name, Err: errors.New("path is outside the tool root")}
}

// joinAccessPath preserves the components of dir so the root can resolve them
// in order. filepath.Join would collapse "link/.." before following the link.
func joinAccessPath(dir, name string) string {
	if dir == "" || os.IsPathSeparator(dir[len(dir)-1]) {
		return dir + name
	}
	return dir + string(filepath.Separator) + name
}

// ResolvePath turns a path a reader typed into the absolute one the filesystem
// knows: "@", "~", relative paths, and the two spellings macOS disagrees with
// itself about are all handled here.
//
// It is exported because the front end resolves an "@path" reference before the
// tools ever see it, and two answers to "which file is this" is one answer too
// many: a reference the reader picked off a screenshot name must resolve to the
// same file the read tool would open.
func ResolvePath(path, cwd string) string {
	return resolveReadPath(path, cwd)
}

// resolveReadPath is resolveToCwd plus recovery for paths the filesystem spells
// differently than a human types them. macOS stores screenshot names with a
// narrow no-break space before AM/PM and file names in NFD, and French
// screenshots use a curly apostrophe. A model copying a name off the screen gets
// the straight variants, so the mismatch is repaired by probing the variants
// before giving up.
func resolveReadPath(path, cwd string) string {
	resolved := resolveToCwd(path, cwd)
	if exists(resolved) {
		return resolved
	}
	for _, candidate := range alternatives(resolved) {
		if candidate != resolved && exists(candidate) {
			return candidate
		}
	}
	return resolved
}

// alternatives lists the spellings tried by resolveReadPath, most likely first.
func alternatives(path string) []string {
	ampm := narrowNoBreakAMPM(path)
	nfd := []rune(normalizeNFD(path))
	curly := curlyQuotes(path)
	return []string{ampm, string(nfd), curly, curlyQuotes(string(nfd))}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func expandTilde(path, home string) (string, bool) {
	if path == "~" {
		return home, true
	}
	if !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		return path, false
	}
	return joinAccessPath(home, path[2:]), true
}

// normalizeUnicodeSpaces maps the unicode space variants that look identical to
// a regular space. Without this a pasted path silently fails to open.
func normalizeUnicodeSpaces(path string) string {
	if !strings.ContainsFunc(path, isUnicodeSpace) {
		return path
	}
	return strings.Map(func(r rune) rune {
		if isUnicodeSpace(r) {
			return ' '
		}
		return r
	}, path)
}

func isUnicodeSpace(r rune) bool {
	switch r {
	case '\u00a0', '\u202f', '\u205f', '\u3000':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// narrowNoBreakAMPM rewrites "Capture 1.20 PM.png" to the narrow no-break space
// variant macOS actually writes.
func narrowNoBreakAMPM(path string) string {
	if !strings.Contains(path, " AM.") && !strings.Contains(path, " PM.") &&
		!strings.Contains(path, " am.") && !strings.Contains(path, " pm.") {
		return path
	}
	var out strings.Builder
	out.Grow(len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == ' ' && i+3 < len(path) {
			marker := strings.ToLower(path[i+1 : i+3])
			if (marker == "am" || marker == "pm") && path[i+3] == '.' {
				out.WriteString("\u202f")
				continue
			}
		}
		out.WriteByte(path[i])
	}
	return out.String()
}

// curlyQuotes replaces ASCII apostrophes with U+2019, the character macOS puts
// in screenshot names like "Capture d'écran".
func curlyQuotes(path string) string {
	return strings.ReplaceAll(path, "'", "\u2019")
}

// normalizeNFD decomposes a string. Go's standard library has no normalization
// package, so this covers the Latin-1 and Latin Extended-A precomposed letters
// that appear in practice. Anything else is passed through unchanged.
func normalizeNFD(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for _, r := range s {
		if parts, ok := nfdTable[r]; ok {
			out.WriteString(parts)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// nfdTable maps precomposed letters to their two-rune decomposition.
var nfdTable = map[rune]string{
	'à': "a\u0300", 'á': "a\u0301", 'â': "a\u0302", 'ã': "a\u0303", 'ä': "a\u0308", 'å': "a\u030a",
	'è': "e\u0300", 'é': "e\u0301", 'ê': "e\u0302", 'ë': "e\u0308",
	'ì': "i\u0300", 'í': "i\u0301", 'î': "i\u0302", 'ï': "i\u0308",
	'ò': "o\u0300", 'ó': "o\u0301", 'ô': "o\u0302", 'õ': "o\u0303", 'ö': "o\u0308",
	'ù': "u\u0300", 'ú': "u\u0301", 'û': "u\u0302", 'ü': "u\u0308",
	'ñ': "n\u0303", 'ç': "c\u0327", 'ý': "y\u0301", 'ÿ': "y\u0308",
	'À': "A\u0300", 'Á': "A\u0301", 'Â': "A\u0302", 'Ã': "A\u0303", 'Ä': "A\u0308", 'Å': "A\u030a",
	'È': "E\u0300", 'É': "E\u0301", 'Ê': "E\u0302", 'Ë': "E\u0308",
	'Ì': "I\u0300", 'Í': "I\u0301", 'Î': "I\u0302", 'Ï': "I\u0308",
	'Ò': "O\u0300", 'Ó': "O\u0301", 'Ô': "O\u0302", 'Õ': "O\u0303", 'Ö': "O\u0308",
	'Ù': "U\u0300", 'Ú': "U\u0301", 'Û': "U\u0302", 'Ü': "U\u0308",
	'Ñ': "N\u0303", 'Ç': "C\u0327", 'Ý': "Y\u0301",
	'ā': "a\u0304", 'ē': "e\u0304", 'ī': "i\u0304", 'ō': "o\u0304", 'ū': "u\u0304",
	'ă': "a\u0306", 'ĕ': "e\u0306", 'ĭ': "i\u0306", 'ŏ': "o\u0306", 'ŭ': "u\u0306",
	'ą': "a\u0328", 'ę': "e\u0328", 'į': "i\u0328", 'ų': "u\u0328",
	'č': "c\u030c", 'š': "s\u030c", 'ž': "z\u030c",
	'Č': "C\u030c", 'Š': "S\u030c", 'Ž': "Z\u030c",
}

// pathErrorText renders a filesystem error the way the Go message reads, with
// the operation stripped so the tool can compose its own sentence.
func pathErrorText(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// isNotExist reports whether err means "no such file or directory".
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// trimSpaceForDisplay keeps control characters out of tool output. A file with
// stray carriage returns should not rewrite the terminal line.
func trimSpaceForDisplay(s string) string {
	return strings.TrimFunc(s, unicode.IsControl)
}

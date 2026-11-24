package main

// File completion and attachment preparation for the two CLI input modes.
// Batch input expands typed references. The TUI stages only explicitly picked
// images and leaves every path visible in the submitted text.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"pi-go/internal/app"
	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

const (
	// attachMaxImageBytes is the largest image that goes into a request as an
	// inline image. It is the ceiling the providers share; nothing here resizes,
	// because resizing is a decoder and an encoder per format and the read tool
	// takes the same route (architecture.md, the image gap).
	attachMaxImageBytes = 5 << 20
	// attachMaxTextBytes is how much of a text file is pasted into the message.
	// It is roughly what one read returns, so an attachment cannot say more than
	// the model could have read itself, and a bigger file is refused with a
	// sentence rather than inlined halfway.
	attachMaxTextBytes = 64 << 10
	// attachCandidates bounds the palette. The list scrolls, but a directory with
	// ten thousand entries should not become ten thousand rows to scroll.
	attachCandidates = 40
)

// refBoundary reports whether the rune before an '@' puts that '@' at the start
// of a word. Anything else — "bob@example.com" — is prose and stays prose.
func refBoundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("([{<\"'`=", r)
}

// parseAttachRefs finds the references in a line, in the order they were typed.
//
// A reference starts at an '@' that begins a word and runs to the next space, or
// to the closing quote when one was opened. A bare '@' is not a reference — it is
// the character that opens the palette — and an unterminated quote is reported
// rather than guessed at: a path with a space in it that lost its closing quote
// is a path the reader meant, and sending the sentence without it would answer a
// question they did not ask.
func parseAttachRefs(line string) ([]string, error) {
	runes := []rune(line)
	refs := make([]string, 0, 1)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '@' || (i > 0 && !refBoundary(runes[i-1])) {
			continue
		}
		if i+1 < len(runes) && runes[i+1] == '"' {
			close := -1
			for j := i + 2; j < len(runes); j++ {
				if runes[j] == '"' {
					close = j
					break
				}
			}
			if close < 0 {
				return nil, fmt.Errorf("@%s is missing its closing quote", string(runes[i+2:]))
			}
			if token := string(runes[i+2 : close]); token != "" {
				refs = append(refs, token)
			}
			i = close
			continue
		}
		j := i + 1
		for j < len(runes) && !unicode.IsSpace(runes[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		refs = append(refs, string(runes[i+1:j]))
		i = j - 1
	}
	return refs, nil
}

// attachFile reads one referenced file and reports the blocks it becomes.
//
// An image becomes a tag plus the image itself, which is what a provider takes
// inline; anything else becomes a tag with the file's text inside it. Both shapes
// are pi's, and the tag is what gives the model a path to talk about when it
// answers: "the file you attached" is not something it can name.
func attachFile(token, cwd string) ([]wire.Content, error) {
	absolute := fs.ResolvePath(token, cwd)
	if _, err := os.Stat(absolute); err != nil && errors.Is(err, os.ErrNotExist) {
		// A reference ends at whitespace, so the punctuation that follows it in
		// prose — "read @main.go." or "(@main.go)" — arrives as part of the
		// path. The exact path is tried first, so a file whose name really ends
		// in one of these is never rewritten; only a miss is retried without
		// them.
		if trimmed := strings.TrimRight(token, ",;:.!?)]}>"); trimmed != token && trimmed != "" {
			if candidate := fs.ResolvePath(trimmed, cwd); stat(candidate) {
				token, absolute = trimmed, candidate
			}
		}
	}
	info, err := os.Stat(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("@%s: no such file (%s)", token, absolute)
		}
		return nil, fmt.Errorf("@%s: %v", token, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("@%s is a directory, not a file", token)
	}
	if size := info.Size(); size > attachMaxImageBytes {
		return nil, fmt.Errorf("@%s is %s; the most that can be attached is %s",
			token, tool.FormatSize(int(size)), tool.FormatSize(attachMaxImageBytes))
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("@%s: %v", token, err)
	}
	tag := fmt.Sprintf(`<file name="%s">`, absolute)
	if mimeType := fs.ImageMimeType(data); mimeType != "" {
		return attachedImage(tag, mimeType, data), nil
	}
	if len(data) > attachMaxTextBytes {
		return nil, fmt.Errorf("@%s is %s; text is attached up to %s — ask for it to be read instead",
			token, tool.FormatSize(len(data)), tool.FormatSize(attachMaxTextBytes))
	}
	return []wire.Content{wire.TextContent(tag + "\n" + string(data) + "\n</file>")}, nil
}

// stat reports whether a path is there to be attached.
func stat(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// attachMessage expands typed file references for non-interactive input.
func attachMessage(line, cwd string) (wire.Message, error) {
	refs, err := parseAttachRefs(line)
	if err != nil {
		return wire.Message{}, err
	}
	if len(refs) == 0 {
		return wire.UserMessage(line), nil
	}
	blocks := []wire.Content{wire.TextContent(line)}
	for _, ref := range refs {
		made, err := attachFile(ref, cwd)
		if err != nil {
			return wire.Message{}, err
		}
		blocks = append(blocks, made...)
	}
	return wire.UserMessageBlocks(blocks...), nil
}

func attachedImage(tag, mimeType string, data []byte) []wire.Content {
	return []wire.Content{
		wire.TextContent(tag + "</file>"),
		wire.ImageContent(base64.StdEncoding.EncodeToString(data), mimeType),
	}
}

// openAttachment opens a regular file. A directory or a device is not an image candidate.
func openAttachment(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	return os.Open(path)
}

func isAttachImage(path string) bool {
	file, err := openAttachment(path)
	if err != nil {
		return false
	}
	defer file.Close()
	// The shared sniffer inspects only the leading bytes, including PNG chunk headers.
	header, err := io.ReadAll(io.LimitReader(file, 8<<10))
	return err == nil && fs.ImageMimeType(header) != ""
}

// interactiveMessage keeps paths in the text and reads only explicitly picked images.
func interactiveMessage(line string, images []string) (wire.Message, error) {
	if len(images) == 0 {
		return wire.UserMessage(line), nil
	}
	blocks := []wire.Content{wire.TextContent(line)}
	for _, path := range images {
		image, err := readSelectedImage(path)
		if err != nil {
			return wire.Message{}, fmt.Errorf("attach image %q: %w", path, err)
		}
		blocks = append(blocks, image...)
	}
	return wire.UserMessageBlocks(blocks...), nil
}

func readSelectedImage(path string) ([]wire.Content, error) {
	file, err := openAttachment(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, attachMaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > attachMaxImageBytes {
		return nil, fmt.Errorf("exceeds %s", tool.FormatSize(attachMaxImageBytes))
	}
	mimeType := fs.ImageMimeType(data)
	if mimeType == "" {
		return nil, errors.New("no longer a supported image")
	}
	return attachedImage(fmt.Sprintf(`<file name="%s">`, path), mimeType, data), nil
}

// attachDir is the directory references resolve against: the session's, not the
// process's. A conversation carries where it was started, and a resumed one can
// be looking at a different tree than the shell that launched atlas.
func attachDir(application *app.App) string {
	if application == nil {
		return "."
	}
	return application.Session().Cwd()
}

// attachSite is the reference being typed at the end of the line: what follows
// the '@', and where the '@' is.
type attachSite struct {
	typed  string // what follows the '@', without an opening quote
	start  int    // rune offset of the '@'
	quoted bool
	closed bool
}

// attachSiteAt finds the reference the reader is still typing: the last '@' that
// begins a word, followed by nothing but the token so far.
//
// Only the end of the line counts. The composer hands over its text and not its
// caret, so a reference being edited in the middle of a sentence is not a
// completion site — which is why Tab in the middle of a line does nothing.
func attachSiteAt(line string) (attachSite, bool) {
	runes := []rune(line)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] != '@' || (i > 0 && !refBoundary(runes[i-1])) {
			continue
		}
		rest := runes[i+1:]
		if len(rest) > 0 && rest[0] == '"' {
			inner := string(rest[1:])
			if end := strings.IndexByte(inner, '"'); end >= 0 {
				if end != len(inner)-1 || end == 0 {
					return attachSite{}, false
				}
				return attachSite{typed: inner[:end], start: i, quoted: true, closed: true}, true
			}
			return attachSite{typed: inner, start: i, quoted: true}, true
		}
		if strings.ContainsFunc(string(rest), unicode.IsSpace) {
			return attachSite{}, false
		}
		return attachSite{typed: string(rest), start: i}, true
	}
	return attachSite{}, false
}

// attachSuggestions is the palette's other half: while a reference is being
// typed, the entries of the directory it points into that still match.
//
// One directory, not a tree: the reader is choosing a name they can see, and
// walking the whole repository on every keystroke would answer a question they
// did not ask while making the keyboard feel heavy.
func attachSuggestions(line, cwd string) []tui.Suggestion {
	return attachSuggestionsFor(line, cwd, false)
}

func attachSuggestionsFor(line, cwd string, subagents bool) []tui.Suggestion {
	site, ok := attachSiteAt(line)
	if !ok {
		return nil
	}
	runes := []rune(line)
	replace := len(runes) - site.start
	var out []tui.Suggestion
	if subagents && !site.quoted {
		out = subagentSuggestions(string(runes[:site.start]), site.typed, replace)
	}
	dir, base := splitAttachPath(site.typed)
	root := cwd
	if dir != "" {
		root = fs.ResolvePath(dir, cwd)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	prefix := strings.ToLower(base)
	for _, entry := range entries {
		name := entry.Name()
		if site.closed && (entry.IsDir() || name != base) {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		// A dot file is a decision, not a default: it appears once the reader
		// has typed the dot.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if len(out) == attachCandidates {
			break
		}
		candidate := attachName(dir, name, entry.IsDir())
		if site.closed || subagents && len(wire.SubagentRoles(strings.TrimRight(candidate, "\"'`"))) > 0 {
			candidate = `@"` + dir + name + `"`
		}
		pick := tui.Suggestion{Name: candidate, About: attachAbout(entry), Replace: replace}
		if !entry.IsDir() {
			pick.Path = fs.ResolvePath(dir+name, cwd)
		}
		out = append(out, pick)
	}
	return out
}

func subagentSuggestions(before, typed string, replace int) []tui.Suggestion {
	if !strings.HasPrefix(wire.SubagentExplorer, typed) && !strings.HasPrefix(wire.SubagentVerifier, typed) {
		return nil
	}
	// Earlier roles cannot stand in for the reference being completed. Preserve
	// quoted path markers so the shared parser can skip them.
	roles := strings.NewReplacer("@"+wire.SubagentExplorer, "@x", "@"+wire.SubagentVerifier, "@x")
	probe := roles.Replace(before) + "@" + wire.SubagentExplorer
	if len(wire.SubagentRoles(probe)) == 0 {
		return nil
	}
	var out []tui.Suggestion
	for _, role := range []tui.Suggestion{
		{Name: wire.SubagentExplorer, About: "subagent · explore code and trace callers", Replace: replace},
		{Name: wire.SubagentVerifier, About: "subagent · verify a claim against evidence", Replace: replace},
	} {
		if strings.HasPrefix(role.Name, typed) {
			role.Name = "@" + role.Name
			out = append(out, role)
		}
	}
	return out
}

// splitAttachPath cuts what has been typed into the directory part, separator
// included, and the name being typed inside it.
func splitAttachPath(typed string) (dir, base string) {
	if i := strings.LastIndexAny(typed, `/\`); i >= 0 {
		return typed[:i+1], typed[i+1:]
	}
	return "", typed
}

// attachName is the text the palette fills in: the typed directory kept as
// typed, so "~" stays "~" and an absolute path stays absolute.
//
// A name with a space in it is quoted, because that is the only form the sender
// reads back as one path. A directory is left with its quote open — the next
// keystroke is the next segment, and the quote closes when a file name is
// finished.
func attachName(dir, name string, isDir bool) string {
	path := dir + name
	if isDir {
		path += "/"
	}
	if !strings.ContainsRune(path, '"') && !strings.ContainsFunc(path, unicode.IsSpace) {
		return "@" + path
	}
	if isDir {
		return `@"` + path
	}
	return `@"` + path + `"`
}

// attachAbout is the palette's second column: what the entry is, or how big.
func attachAbout(entry os.DirEntry) string {
	if entry.IsDir() {
		return "directory"
	}
	info, err := entry.Info()
	if err != nil {
		return ""
	}
	return tool.FormatSize(int(info.Size()))
}

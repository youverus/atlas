package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// exportHeader is the front matter of an exported transcript.
type exportHeader struct {
	name     string
	session  string
	cwd      string
	model    string
	exported time.Time
}

// exportFormat is the document an export writes. The three are different
// answers to different questions, which is why one is not enough: Markdown is
// read in an editor, HTML is read in a browser, and JSONL is not read at all —
// it is a session file, so `-resume` can open it again.
type exportFormat string

const (
	formatMarkdown exportFormat = "md"
	formatHTML     exportFormat = "html"
	formatJSONL    exportFormat = "jsonl"
)

// extension is the suffix the format is known by, which is the same word the
// default file name uses.
func (f exportFormat) extension() string { return "." + string(f) }

// exportArg reads the argument of /export: where the file goes and what it
// holds.
//
// The extension decides the format — one rule, so the name of a file is never a
// lie about its contents. A bare --md / --html / --jsonl is the shorthand for
// "the default name, in that format", which is a request a path cannot express
// without repeating the session id.
func exportArg(arg string) (exportFormat, string, error) {
	arg = strings.TrimSpace(arg)
	switch {
	case arg == "":
		return formatMarkdown, "", nil
	case arg == "--md", arg == "--markdown":
		return formatMarkdown, "", nil
	case arg == "--html":
		return formatHTML, "", nil
	case arg == "--jsonl", arg == "--json":
		return formatJSONL, "", nil
	case strings.HasPrefix(arg, "--"):
		return "", "", unsupportedExportFormat(arg)
	}
	switch strings.ToLower(filepath.Ext(arg)) {
	case ".md", ".markdown":
		return formatMarkdown, arg, nil
	case ".html", ".htm":
		return formatHTML, arg, nil
	case ".jsonl", ".ndjson":
		return formatJSONL, arg, nil
	}
	return "", "", unsupportedExportFormat(arg)
}

// unsupportedExportFormat answers both ways of naming a format atlas cannot
// write — an unknown option and an unknown extension — with one sentence, so the
// user learns the three that exist instead of the one that does not.
func unsupportedExportFormat(arg string) error {
	return fmt.Errorf("export: %s is not a format; name a .md, .html or .jsonl file, or pass --md, --html or --jsonl for the default name", arg)
}

// writeExport renders the session and reports where it went. An empty target
// names the file after the session inside the session's cwd, so exporting the
// same session twice updates one file instead of piling up timestamped copies.
func writeExport(out io.Writer, application *app.App, arg string) error {
	format, target, err := exportArg(arg)
	if err != nil {
		return err
	}
	manager := application.Session()
	path, err := exportPath(manager.Cwd(), target, manager.ID(), format)
	if err != nil {
		return err
	}
	// The parent directory is the user's to create. session.SaveFile would make
	// the tree, which is why the check is here: a mistyped path should look like
	// a mistyped path, not silently become an empty directory somewhere.
	if dir := filepath.Dir(path); dir != "" {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("export: %s does not exist; create it first", dir)
		}
	}

	header := exportHeader{
		name:     application.Name(),
		session:  manager.ID(),
		cwd:      manager.Cwd(),
		model:    application.Model().Provider + "/" + application.Model().ID,
		exported: time.Now(),
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writeErr := writeExportBody(file, format, header, manager, application.Messages())
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "exported: %s (%d bytes)\n", path, info.Size())
	return nil
}

// writeExportBody picks the document. The JSONL one takes the session instead of
// the projected messages: it is not a printout of the conversation, it is the
// session file itself, flattened to the branch being read.
func writeExportBody(w io.Writer, format exportFormat, header exportHeader, manager *session.Manager, messages []wire.Message) error {
	switch format {
	case formatHTML:
		return exportHTML(w, header, messages)
	case formatJSONL:
		return exportJSONL(w, manager)
	default:
		return exportMarkdown(w, header, messages)
	}
}

// exportJSONL writes the current branch as a session file: the header, then one
// entry per line.
//
// It reuses the store's own encoder instead of inventing a shape for the export,
// so the file loads again — `atlas -resume export.jsonl`, or pi itself. That is
// the difference between this format and the other two: they are documents about
// the conversation, this one is the conversation.
func exportJSONL(w io.Writer, manager *session.Manager) error {
	data, err := session.EncodeFile(&session.File{
		Header:  manager.Header(),
		Entries: relinkBranch(manager.Path("")),
	})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// relinkBranch rewrites parentId so every entry points at the one before it. A
// branch is a chain by construction, so this only shows at the root, where
// parentId has to be null rather than whatever the first entry inherited from a
// branch that is no longer in the file.
func relinkBranch(path []session.Entry) []session.Entry {
	linked := make([]session.Entry, len(path))
	var parent *string
	for i, entry := range path {
		linked[i] = entry
		linked[i].SetParentID(parent)
		id := entry.ID
		parent = &id
	}
	return linked
}

// exportMarkdown renders a transcript as one Markdown document.
//
// The system message is left out: it is the same in every session of a project
// and would be the largest part of the file. Everything else is kept, including
// thinking and tool traffic, because an export is what is left to read when the
// terminal that showed it is gone.
func exportMarkdown(w io.Writer, header exportHeader, messages []wire.Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", htmlTitle(header))
	fmt.Fprintf(&b, "- session: %s\n", header.session)
	if header.cwd != "" {
		fmt.Fprintf(&b, "- cwd: %s\n", header.cwd)
	}
	if header.model != "" {
		fmt.Fprintf(&b, "- model: %s\n", header.model)
	}
	fmt.Fprintf(&b, "- messages: %d\n", conversationLength(messages))
	fmt.Fprintf(&b, "- exported: %s\n", header.exported.Format(time.RFC3339))
	b.WriteString("\n---\n\n")

	for _, message := range messages {
		switch message.Role {
		case wire.RoleUser:
			b.WriteString("## User\n\n")
			writeBlocks(&b, message.Content.Items())
		case wire.RoleAssistant:
			b.WriteString("## Assistant\n\n")
			writeAssistant(&b, message)
		case wire.RoleToolResult:
			writeToolResult(&b, message)
		}
	}
	_, err := io.WriteString(w, strings.TrimRight(b.String(), "\n")+"\n")
	return err
}

// conversationLength counts the messages the document will contain, so the
// header does not promise the system message back.
func conversationLength(messages []wire.Message) int {
	count := 0
	for _, message := range messages {
		if message.Role != wire.RoleSystem {
			count++
		}
	}
	return count
}

// imageNote names an image block in one wording, so the two documents agree
// about what a picture was.
func imageNote(block wire.Content) string {
	mime := block.MIMEType
	if mime == "" {
		mime = "image"
	}
	return fmt.Sprintf("[image: %s]", mime)
}

// writeAssistant renders one assistant message: thinking first, then the
// answer, then the calls the answer made. The order matches the order they were
// produced in, which is the order a reader needs to follow the turn.
func writeAssistant(b *strings.Builder, message wire.Message) {
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentToolCall:
			fmt.Fprintf(b, "**Tool call: %s**\n\n", block.Name)
			fence(b, "json", prettyJSON(block.Arguments))
		default:
			writeBlock(b, block)
		}
	}
	if message.StopReason == wire.StopError && message.ErrorMessage != "" {
		fmt.Fprintf(b, "*error: %s*\n\n", message.ErrorMessage)
	}
}

// writeToolResult renders the result of a tool call. It gets its own heading
// rather than an indented block under the call, because the two are separate
// messages in the transcript and a long result would otherwise read as part of
// the request.
func writeToolResult(b *strings.Builder, message wire.Message) {
	name := message.ToolName
	if name == "" {
		name = "tool"
	}
	if message.IsErrorResult() {
		fmt.Fprintf(b, "## Tool result: %s (error)\n\n", name)
	} else {
		fmt.Fprintf(b, "## Tool result: %s\n\n", name)
	}

	var parts []string
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case wire.ContentImage:
			parts = append(parts, imageNote(block))
		}
	}
	body := strings.TrimRight(strings.Join(parts, "\n\n"), "\n")
	if body == "" {
		body = "(no output)"
	}
	fence(b, "text", body)
}

func writeBlocks(b *strings.Builder, blocks []wire.Content) {
	for _, block := range blocks {
		writeBlock(b, block)
	}
}

func writeBlock(b *strings.Builder, block wire.Content) {
	switch block.Type {
	case wire.ContentText:
		if text := strings.TrimSpace(block.Text); text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	case wire.ContentThinking:
		writeThinking(b, block)
	case wire.ContentImage:
		fmt.Fprintf(b, "*%s*\n\n", imageNote(block))
	}
}

// writeThinking folds a reasoning block into a collapsible section. Thinking is
// long and usually not what someone came to the file for, but dropping it would
// make the export a different conversation from the one that ran.
func writeThinking(b *strings.Builder, block wire.Content) {
	body := strings.TrimSpace(block.Thinking)
	if body == "" {
		if block.Redacted == nil || !*block.Redacted {
			return
		}
		body = "(redacted by the provider)"
	}
	b.WriteString("<details>\n<summary>Thinking</summary>\n\n")
	b.WriteString(body)
	b.WriteString("\n\n</details>\n\n")
}

// fence writes a fenced code block whose fence is longer than any backtick run
// in the body, so tool output that itself contains a fence still round trips.
// The language word is not decoration: GitHub needs something before the
// newline to treat the block as code.
func fence(b *strings.Builder, language, body string) {
	size := backtickRun(body) + 1
	if size < 3 {
		size = 3
	}
	ticks := strings.Repeat("`", size)
	b.WriteString(ticks + language + "\n")
	b.WriteString(body)
	b.WriteString("\n" + ticks + "\n\n")
}

// backtickRun reports the longest run of backticks in body.
func backtickRun(body string) int {
	longest, run := 0, 0
	for _, r := range body {
		if r != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}
	return longest
}

// prettyJSON indents tool arguments for reading. An argument blob that is not
// valid JSON is written as it came: an export must not lose the evidence of a
// malformed call.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// exportPath resolves the target: the argument if there is one, otherwise a
// name derived from the session id and the format. A relative path is relative
// to the session cwd, which is where the user thinks they are.
func exportPath(cwd, target, sessionID string, format exportFormat) (string, error) {
	if target == "" {
		target = "atlas-session-" + shortSessionID(sessionID) + format.extension()
	}
	expanded, err := config.ExpandHome(target)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(cwd, expanded)
	}
	return filepath.Abs(expanded)
}

// shortSessionID is the eight-character prefix the footer shows, so the file
// name of an export matches the session segment of the status line.
func shortSessionID(id string) string {
	if id == "" {
		return "unknown"
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

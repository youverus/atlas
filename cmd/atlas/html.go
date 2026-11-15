package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"pi-go/internal/wire"
)

// exportHTML renders the transcript as one self-contained HTML document.
//
// Self-contained is the requirement, not a preference: a document that links to
// a stylesheet somewhere renders differently every time it is opened, and one
// that links to a script is a document that can be made to do something. So the
// stylesheet is inline, there is no JavaScript, and nothing is fetched when the
// page loads.
//
// What it does not carry is the images in the session. They are named where they
// appeared, as the Markdown export names them: a screenshot is megabytes, and an
// export is a document to read, not an archive to hand over.
func exportHTML(w io.Writer, header exportHeader, messages []wire.Message) error {
	var b strings.Builder
	writeHTMLHead(&b, header)
	b.WriteString("<main>\n")
	if conversationLength(messages) == 0 {
		b.WriteString("<p class=\"empty\">This session holds no messages.</p>\n")
	}
	for _, message := range messages {
		switch message.Role {
		case wire.RoleUser:
			fmt.Fprintf(&b, "<section class=\"message user\">\n<h2>User</h2>\n")
			writeHTMLBlocks(&b, message.Content.Items())
			b.WriteString("</section>\n")
		case wire.RoleAssistant:
			writeHTMLAssistant(&b, message)
		case wire.RoleToolResult:
			writeHTMLToolResult(&b, message)
		}
	}
	b.WriteString("</main>\n")
	fmt.Fprintf(&b, "<footer class=\"session\"><p>atlas %s</p></footer>\n", escapeHTML(version))
	b.WriteString("</body>\n</html>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// htmlTitle is the heading the document opens with, by the same rule the
// Markdown export uses: the session's name when it has one, its id prefix
// otherwise, so two exports of one session are recognizable side by side.
func htmlTitle(header exportHeader) string {
	if header.name != "" {
		return header.name
	}
	return "atlas session " + shortSessionID(header.session)
}

func writeHTMLHead(b *strings.Builder, header exportHeader) {
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(b, "<title>%s</title>\n", escapeHTML(htmlTitle(header)))
	b.WriteString("<style>\n")
	b.WriteString(htmlStyles)
	b.WriteString("</style>\n</head>\n<body>\n<header class=\"session\">\n")
	fmt.Fprintf(b, "<h1>%s</h1>\n", escapeHTML(htmlTitle(header)))
	b.WriteString("<dl class=\"meta\">\n")
	writeHTMLMeta(b, "session", header.session)
	writeHTMLMeta(b, "cwd", header.cwd)
	writeHTMLMeta(b, "model", header.model)
	writeHTMLMeta(b, "exported", header.exported.Format(time.RFC3339))
	b.WriteString("</dl>\n</header>\n")
}

func writeHTMLMeta(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "<dt>%s</dt><dd>%s</dd>\n", name, escapeHTML(value))
}

// writeHTMLAssistant renders one answer: thinking folded away, the text, then
// the calls it made — the order they were produced in, which is the order a
// reader needs to follow the turn.
func writeHTMLAssistant(b *strings.Builder, message wire.Message) {
	b.WriteString("<section class=\"message assistant\">\n<h2>Assistant</h2>\n")
	for _, block := range message.Content.Items() {
		if block.Type == wire.ContentToolCall {
			writeHTMLCall(b, block)
			continue
		}
		if block.Type == wire.ContentThinking && writeHTMLThinking(b, block) {
			continue
		}
		writeHTMLBlock(b, block)
	}
	if message.StopReason == wire.StopError && message.ErrorMessage != "" {
		fmt.Fprintf(b, "<p class=\"error\">error: %s</p>\n", escapeHTML(message.ErrorMessage))
	}
	b.WriteString("</section>\n")
}

// writeHTMLThinking folds reasoning into a collapsed block. It is part of the
// conversation, but it is usually not what someone opened the file for.
func writeHTMLThinking(b *strings.Builder, block wire.Content) bool {
	body := strings.TrimSpace(block.Thinking)
	if body == "" {
		if block.Redacted == nil || !*block.Redacted {
			return false
		}
		body = "(redacted by the provider)"
	}
	b.WriteString("<details class=\"thinking\">\n<summary>Thinking</summary>\n<div class=\"body\">\n")
	writeMarkdown(b, body)
	b.WriteString("</div>\n</details>\n")
	return true
}

func writeHTMLCall(b *strings.Builder, block wire.Content) {
	fmt.Fprintf(b, "<div class=\"call\">\n<p class=\"call-name\">tool call: %s</p>\n", escapeHTML(block.Name))
	b.WriteString("<pre><code class=\"language-json\">")
	writeEscaped(b, prettyJSON(block.Arguments))
	b.WriteString("</code></pre>\n</div>\n")
}

// writeHTMLToolResult gives a result its own section rather than an indented
// block under the call: the two are separate messages, and a long result would
// otherwise read as part of the request.
func writeHTMLToolResult(b *strings.Builder, message wire.Message) {
	name := message.ToolName
	if name == "" {
		name = "tool"
	}
	b.WriteString("<section class=\"message result\">\n")
	if message.IsErrorResult() {
		fmt.Fprintf(b, "<h2>Tool result: %s (error)</h2>\n", escapeHTML(name))
	} else {
		fmt.Fprintf(b, "<h2>Tool result: %s</h2>\n", escapeHTML(name))
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
	b.WriteString("<pre><code>")
	writeEscaped(b, body)
	if body == "" {
		b.WriteString("(no output)")
	}
	b.WriteString("</code></pre>\n</section>\n")
}

// writeHTMLBlocks renders a message whose content is prose, which is the user
// side of the conversation.
func writeHTMLBlocks(b *strings.Builder, blocks []wire.Content) {
	for _, block := range blocks {
		writeHTMLBlock(b, block)
	}
}

// writeHTMLBlock renders one content block. Text is the model's Markdown and is
// parsed as such; everything else is named, not printed.
func writeHTMLBlock(b *strings.Builder, block wire.Content) {
	switch block.Type {
	case wire.ContentText:
		text := strings.TrimSpace(block.Text)
		if text == "" {
			return
		}
		b.WriteString("<div class=\"body\">\n")
		writeMarkdown(b, text)
		b.WriteString("</div>\n")
	case wire.ContentImage:
		fmt.Fprintf(b, "<p class=\"note\">%s</p>\n", escapeHTML(imageNote(block)))
	}
}

// htmlStyles is the whole presentation layer of an export: no font loading, no
// script, and a dark variant that follows the reader's system setting.
const htmlStyles = `:root {
  color-scheme: light dark;
  --fg: #1f2328;
  --muted: #656d76;
  --line: #d1d9e0;
  --bg: #ffffff;
  --code: #f6f8fa;
  --accent: #0969da;
}
@media (prefers-color-scheme: dark) {
  :root {
    --fg: #e6edf3;
    --muted: #9198a1;
    --line: #30363d;
    --bg: #0d1117;
    --code: #161b22;
    --accent: #4493f8;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans", Helvetica, Arial, sans-serif;
  -webkit-text-size-adjust: 100%;
}
header.session, main, footer.session { max-width: 48rem; margin: 0 auto; padding: 0 1.25rem; }
header.session { padding-top: 2.5rem; }
h1 { font-size: 1.5rem; line-height: 1.3; margin: 0 0 1rem; }
dl.meta {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 0.25rem 1rem;
  margin: 0 0 1.5rem;
  color: var(--muted);
  font-size: 0.875rem;
}
dl.meta dt { font-weight: 600; }
dl.meta dd {
  margin: 0;
  overflow-wrap: anywhere;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
section.message { border-top: 1px solid var(--line); padding: 1.5rem 0; }
section.message h2 {
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--muted);
  margin: 0 0 0.75rem;
}
.body > *:first-child { margin-top: 0; }
.body > *:last-child { margin-bottom: 0; }
p { margin: 0 0 1rem; }
h2, h3, h4, h5, h6 { line-height: 1.3; margin: 1.5rem 0 0.75rem; }
ul, ol { margin: 0 0 1rem; padding-left: 1.5rem; }
li { margin: 0.25rem 0; }
li > ul, li > ol { margin: 0.25rem 0; }
blockquote {
  margin: 0 0 1rem;
  padding: 0 1rem;
  border-left: 3px solid var(--line);
  color: var(--muted);
}
hr { border: 0; border-top: 1px solid var(--line); margin: 1.5rem 0; }
a { color: var(--accent); }
a.image::before { content: "image: "; color: var(--muted); }
pre {
  background: var(--code);
  border: 1px solid var(--line);
  border-radius: 6px;
  padding: 0.75rem 1rem;
  margin: 0 0 1rem;
  overflow-x: auto;
}
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.875em;
}
:not(pre) > code {
  background: var(--code);
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 0.1em 0.35em;
}
table {
  border-collapse: collapse;
  width: 100%;
  margin: 0 0 1rem;
  font-size: 0.9375rem;
}
th, td {
  border: 1px solid var(--line);
  padding: 0.4rem 0.6rem;
  text-align: left;
  vertical-align: top;
}
th { background: var(--code); }
th.right, td.right { text-align: right; }
th.center, td.center { text-align: center; }
details.thinking { margin: 0 0 1rem; color: var(--muted); }
details.thinking summary { cursor: pointer; font-size: 0.875rem; }
div.call { margin: 0 0 1rem; }
p.call-name {
  margin: 0 0 0.25rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.8125rem;
  color: var(--muted);
}
p.error { color: #cf222e; }
@media (prefers-color-scheme: dark) { p.error { color: #f85149; } }
p.note { color: var(--muted); font-style: italic; }
p.empty { color: var(--muted); }
footer.session { color: var(--muted); font-size: 0.8125rem; padding: 2rem 1.25rem 4rem; }
`

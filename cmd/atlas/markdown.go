package main

import (
	"fmt"
	"strings"
)

// writeMarkdown renders the subset of Markdown an answer actually uses as HTML.
//
// Hand-written, for the same reason the frontmatter reader in internal/skill is:
// the subset is small, and a module for one file format brings a version to pin,
// a licence to read and a family in tests/deps — all for tables and fenced code.
// What it covers is paragraphs, headings, fenced code, tables, lists, quotes,
// rules and inline code, emphasis, strikethrough and links. What it does not
// cover — reference links, HTML blocks, setext headings, footnotes, nested inline
// emphasis — is left as its own text rather than guessed at: a document that
// quietly drops a paragraph is worse than one that shows the source.
//
// Nothing here can fail. Malformed input renders as text, because an export that
// refuses to write a file the session already recorded would be losing the
// evidence.
func writeMarkdown(b *strings.Builder, text string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); {
		i = writeMarkdownBlock(b, lines, i)
	}
}

// writeMarkdownBlock renders the block that starts on line i and returns the
// first line it did not consume.
func writeMarkdownBlock(b *strings.Builder, lines []string, i int) int {
	line, trimmed := lines[i], strings.TrimSpace(lines[i])
	switch {
	case trimmed == "":
		return i + 1
	case isFence(line):
		return writeCodeFence(b, lines, i)
	case headingLevel(trimmed) > 0:
		return writeHeading(b, trimmed, i)
	case isRule(trimmed):
		b.WriteString("<hr>\n")
		return i + 1
	case strings.HasPrefix(trimmed, ">"):
		return writeQuote(b, lines, i)
	case isTableStart(lines, i):
		return writeTable(b, lines, i)
	}
	if _, ok := parseListMarker(line); ok {
		return writeList(b, lines, i)
	}
	return writeParagraph(b, lines, i)
}

// startsBlock reports whether line i begins a block of its own, which is where a
// paragraph has to stop. It is the same question writeMarkdownBlock asks, minus
// the paragraph fallback.
func startsBlock(lines []string, i int) bool {
	line, trimmed := lines[i], strings.TrimSpace(lines[i])
	switch {
	case trimmed == "":
		return true
	case isFence(line):
		return true
	case headingLevel(trimmed) > 0:
		return true
	case isRule(trimmed):
		return true
	case strings.HasPrefix(trimmed, ">"):
		return true
	}
	if _, ok := parseListMarker(line); ok {
		return true
	}
	return isTableStart(lines, i)
}

// fenceRun reports the fence character, its length and the info string of a line
// that opens or closes a fenced code block. A run shorter than three, or one
// that does not start the line, is not a fence.
func fenceRun(line string) (byte, int, string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(trimmed) < 3 {
		return 0, 0, ""
	}
	char := trimmed[0]
	if char != '`' && char != '~' {
		return 0, 0, ""
	}
	run := 0
	for run < len(trimmed) && trimmed[run] == char {
		run++
	}
	if run < 3 {
		return 0, 0, ""
	}
	return char, run, strings.TrimSpace(trimmed[run:])
}

func isFence(line string) bool {
	char, _, _ := fenceRun(line)
	return char != 0
}

// closingFence reports whether line ends the fence opened by char repeated run
// times: the same character, at least as many times, and nothing else on it.
func closingFence(line string, char byte, run int) bool {
	trimmed := strings.TrimSpace(line)
	count := 0
	for count < len(trimmed) && trimmed[count] == char {
		count++
	}
	return count >= run && count == len(trimmed)
}

// writeCodeFence copies a fenced block out verbatim. An unclosed fence runs to
// the end of the text, which is what CommonMark does and what a half-streamed
// answer needs.
func writeCodeFence(b *strings.Builder, lines []string, start int) int {
	char, run, info := fenceRun(lines[start])
	i := start + 1
	var body []string
	for i < len(lines) {
		if closingFence(lines[i], char, run) {
			i++
			break
		}
		body = append(body, lines[i])
		i++
	}
	b.WriteString("<pre><code")
	if class := languageClass(info); class != "" {
		b.WriteString(` class="language-` + class + `"`)
	}
	b.WriteString(">")
	writeEscaped(b, strings.Join(body, "\n"))
	b.WriteString("</code></pre>\n")
	return i
}

// languageClass keeps the first word of the info string, and only the characters
// a language name is made of. The value lands in an attribute, so a fence like
// "```go\" onmouseover=x" must not become one.
func languageClass(info string) string {
	if at := strings.IndexByte(info, ' '); at >= 0 {
		info = info[:at]
	}
	var b strings.Builder
	for i := 0; i < len(info); i++ {
		c := info[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '+', c == '#', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		}
	}
	return b.String()
}

// headingLevel reports the number of leading #, or 0 when the line is not a
// heading. Seven or more is not a heading, and neither is a run with no space
// after it: "#hashtag" is a word.
func headingLevel(trimmed string) int {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0
	}
	if level == len(trimmed) {
		return level
	}
	if trimmed[level] != ' ' && trimmed[level] != '\t' {
		return 0
	}
	return level
}

func writeHeading(b *strings.Builder, trimmed string, i int) int {
	level := headingLevel(trimmed)
	text := strings.TrimSpace(trimmed[level:])
	fmt.Fprintf(b, "<h%d>", level)
	writeInline(b, text)
	fmt.Fprintf(b, "</h%d>\n", level)
	return i + 1
}

// isRule reports a thematic break: three or more of the same -, * or _ with
// nothing else on the line. Spaces between them are allowed, so "- - -" counts.
func isRule(trimmed string) bool {
	dense := strings.ReplaceAll(trimmed, " ", "")
	if len(dense) < 3 {
		return false
	}
	char := dense[0]
	if char != '-' && char != '*' && char != '_' {
		return false
	}
	for i := 1; i < len(dense); i++ {
		if dense[i] != char {
			return false
		}
	}
	return true
}

// writeQuote folds a run of "> " lines into a blockquote. The body is rendered
// as Markdown again, so a quote can hold a list or a second quote.
func writeQuote(b *strings.Builder, lines []string, start int) int {
	i := start
	var body []string
	for i < len(lines) {
		trimmed := strings.TrimLeft(lines[i], " ")
		if !strings.HasPrefix(trimmed, ">") {
			break
		}
		body = append(body, strings.TrimPrefix(trimmed[1:], " "))
		i++
	}
	b.WriteString("<blockquote>\n")
	writeMarkdown(b, strings.Join(body, "\n"))
	b.WriteString("</blockquote>\n")
	return i
}

// isTableStart reports a header row with a delimiter row under it and the same
// number of cells. A delimiter row with no header, or one whose cell count
// disagrees, is text: guessing which columns were meant would move every value
// one column to the left.
func isTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") {
		return false
	}
	head := splitRow(lines[i])
	aligns := splitRow(lines[i+1])
	if len(head) == 0 || len(head) != len(aligns) {
		return false
	}
	for _, cell := range aligns {
		if !isDelimiterCell(cell) {
			return false
		}
	}
	return true
}

// splitRow breaks a table row into trimmed cells. "\|" is a literal pipe inside
// a cell; a pipe inside a code span still splits, which is the one place this
// reader is simpler than CommonMark.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cell.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(cell.String()))
}

// isDelimiterCell reports "---", ":--", "--:" or ":-:".
func isDelimiterCell(cell string) bool {
	if cell == "" {
		return false
	}
	cell = strings.TrimPrefix(cell, ":")
	cell = strings.TrimSuffix(cell, ":")
	if cell == "" {
		return false
	}
	for i := 0; i < len(cell); i++ {
		if cell[i] != '-' {
			return false
		}
	}
	return true
}

// writeTable renders a header row, its delimiter row and the body rows under it.
// Rows narrower or wider than the header are fitted to it, so a ragged paste
// still lines up in one grid.
func writeTable(b *strings.Builder, lines []string, start int) int {
	head := splitRow(lines[start])
	aligns := tableAlignment(splitRow(lines[start+1]))
	i := start + 2
	var rows [][]string
	for i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
		rows = append(rows, fitCells(splitRow(lines[i]), len(head)))
		i++
	}

	b.WriteString("<table>\n<thead>\n<tr>")
	for cell := range head {
		writeCell(b, "th", aligns[cell], head[cell])
	}
	b.WriteString("</tr>\n</thead>\n")
	if len(rows) > 0 {
		b.WriteString("<tbody>\n")
		for _, row := range rows {
			b.WriteString("<tr>")
			for cell := range head {
				writeCell(b, "td", aligns[cell], row[cell])
			}
			b.WriteString("</tr>\n")
		}
		b.WriteString("</tbody>\n")
	}
	b.WriteString("</table>\n")
	return i
}

// tableAlignment turns the delimiter row into one class name per column. The
// class goes in the stylesheet rather than in a style attribute, so a document
// keeps one place that decides how a right-aligned cell looks.
func tableAlignment(delimiters []string) []string {
	aligns := make([]string, len(delimiters))
	for i, cell := range delimiters {
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			aligns[i] = "center"
		case right:
			aligns[i] = "right"
		}
	}
	return aligns
}

func writeCell(b *strings.Builder, tag, align, text string) {
	b.WriteString("<" + tag)
	if align != "" {
		b.WriteString(` class="` + align + `"`)
	}
	b.WriteString(">")
	writeInline(b, text)
	b.WriteString("</" + tag + ">")
}

// fitCells pads or truncates a row to width. A short row gets empty cells so the
// grid stays a grid; a long one loses the extra cells, which is what a reader of
// the source would have to do anyway.
func fitCells(cells []string, width int) []string {
	if len(cells) > width {
		return cells[:width]
	}
	for len(cells) < width {
		cells = append(cells, "")
	}
	return cells
}

// listMarker is the head of a list item: how far it is indented, whether it is
// ordered, the number it carries, the width of the marker plus its trailing
// space, and the text that follows.
type listMarker struct {
	indent  int
	ordered bool
	number  int
	width   int
	content string
}

// parseListMarker reads "- x", "* x", "+ x", "1. x" and "1) x". A marker must be
// followed by a space, so "-5 degrees" stays a paragraph.
func parseListMarker(line string) (listMarker, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	rest := line[indent:]
	if len(rest) < 2 {
		return listMarker{}, false
	}
	if (rest[0] == '-' || rest[0] == '*' || rest[0] == '+') && (rest[1] == ' ' || rest[1] == '\t') {
		return listMarker{
			indent:  indent,
			width:   indent + 2,
			content: strings.TrimLeft(rest[2:], " \t"),
		}, true
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits+1 >= len(rest) {
		return listMarker{}, false
	}
	if (rest[digits] != '.' && rest[digits] != ')') || (rest[digits+1] != ' ' && rest[digits+1] != '\t') {
		return listMarker{}, false
	}
	number := 0
	for i := 0; i < digits; i++ {
		number = number*10 + int(rest[i]-'0')
	}
	return listMarker{
		indent:  indent,
		ordered: true,
		number:  number,
		width:   indent + digits + 2,
		content: strings.TrimLeft(rest[digits+2:], " \t"),
	}, true
}

// writeList renders consecutive items of one kind at one indentation. An item
// whose text continues on later lines — including a nested list, which is itself
// Markdown — is rendered as blocks inside its <li>; a one-line item keeps its
// text inline, because a tight list is a list without paragraph margins around
// every item.
func writeList(b *strings.Builder, lines []string, start int) int {
	first, _ := parseListMarker(lines[start])
	tag, opening := "ul", "<ul>"
	if first.ordered {
		tag, opening = "ol", "<ol>"
		if first.number != 1 {
			opening = fmt.Sprintf(`<ol start="%d">`, first.number)
		}
	}
	b.WriteString(opening + "\n")

	i := start
	for i < len(lines) {
		item, ok := parseListMarker(lines[i])
		if !ok || item.indent != first.indent || item.ordered != first.ordered {
			break
		}
		body := []string{item.content}
		i++
		for i < len(lines) {
			if strings.TrimSpace(lines[i]) == "" {
				break
			}
			next, isItem := parseListMarker(lines[i])
			if isItem && next.indent <= first.indent {
				break
			}
			body = append(body, dedent(lines[i], item.width))
			i++
		}
		b.WriteString("<li>")
		if len(body) == 1 {
			writeInline(b, strings.TrimSpace(body[0]))
		} else {
			b.WriteString("\n")
			writeMarkdown(b, strings.Join(body, "\n"))
		}
		b.WriteString("</li>\n")
	}
	b.WriteString("</" + tag + ">\n")
	return i
}

// dedent removes up to width leading spaces, which puts a continuation line at
// the column its item's text starts at.
func dedent(line string, width int) string {
	cut := 0
	for cut < width && cut < len(line) && line[cut] == ' ' {
		cut++
	}
	return line[cut:]
}

// writeParagraph collects lines until the next block and renders them as one
// paragraph. Soft line breaks become spaces, which is what Markdown says they
// are and what prose written one sentence per line expects.
func writeParagraph(b *strings.Builder, lines []string, start int) int {
	i := start
	var body []string
	for i < len(lines) {
		if i > start && startsBlock(lines, i) {
			break
		}
		body = append(body, strings.TrimSpace(lines[i]))
		i++
	}
	b.WriteString("<p>")
	writeInline(b, strings.Join(body, " "))
	b.WriteString("</p>\n")
	return i
}

// writeInline renders one run of text: escapes, code spans, emphasis,
// strikethrough and links, with everything else copied through escaped.
func writeInline(b *strings.Builder, text string) {
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '\\' && i+1 < len(text) && isMarkdownPunctuation(text[i+1]):
			writeEscapedByte(b, text[i+1])
			i += 2
		case c == '`':
			i = writeCodeSpan(b, text, i)
		case c == '*' || c == '_':
			width, tag := 1, "em"
			if i+1 < len(text) && text[i+1] == c {
				width, tag = 2, "strong"
			}
			i = writeDelimited(b, text, i, width, tag)
		case c == '~' && strings.HasPrefix(text[i:], "~~"):
			i = writeDelimited(b, text, i, 2, "del")
		case c == '!' && i+1 < len(text) && text[i+1] == '[':
			i = writeLink(b, text, i, true)
		case c == '[':
			i = writeLink(b, text, i, false)
		default:
			writeEscapedByte(b, c)
			i++
		}
	}
}

// writeCodeSpan renders `code`. A run of backticks with no partner stays a
// literal run, which is how a half-written answer keeps its backticks.
func writeCodeSpan(b *strings.Builder, text string, start int) int {
	run := 0
	for start+run < len(text) && text[start+run] == '`' {
		run++
	}
	for i := start + run; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		length := 0
		for i+length < len(text) && text[i+length] == '`' {
			length++
		}
		if length == run {
			body := text[start+run : i]
			if len(body) > 2 && strings.HasPrefix(body, " ") && strings.HasSuffix(body, " ") {
				body = body[1 : len(body)-1]
			}
			b.WriteString("<code>")
			writeEscaped(b, body)
			b.WriteString("</code>")
			return i + length
		}
		i += length
	}
	writeEscaped(b, text[start:start+run])
	return start + run
}

// writeDelimited renders emphasis and strikethrough. A delimiter that cannot
// pair up — nothing after it, or a space on either side of the text — is written
// as itself, so "2 * 3 * 4" keeps its asterisks. An underscore inside a word is
// never a delimiter: snake_case_names are identifiers, not emphasis.
func writeDelimited(b *strings.Builder, text string, start, width int, tag string) int {
	delimiter := text[start : start+width]
	body := text[start+width:]
	at := strings.Index(body, delimiter)
	if at <= 0 || isSpaceByte(body[0]) || isSpaceByte(body[at-1]) {
		writeEscaped(b, delimiter)
		return start + width
	}
	if delimiter[0] == '_' {
		before := start > 0 && isWordByte(text[start-1])
		after := at+width < len(body) && isWordByte(body[at+width])
		if before || after {
			writeEscaped(b, delimiter)
			return start + width
		}
	}
	b.WriteString("<" + tag + ">")
	writeInline(b, body[:at])
	b.WriteString("</" + tag + ">")
	return start + width + at + width
}

// writeLink renders "[label](url)". An image — "![alt](src)" — becomes a link
// too: an export is opened on a machine that may be offline, and an <img> would
// go looking for the file or the network the moment the page loads.
func writeLink(b *strings.Builder, text string, at int, image bool) int {
	bracket := at
	if image {
		bracket++
	}
	end := strings.IndexByte(text[bracket:], ']')
	if end < 0 {
		writeEscapedByte(b, text[at])
		return at + 1
	}
	start := bracket + end + 1
	if start >= len(text) || text[start] != '(' {
		writeEscapedByte(b, text[at])
		return at + 1
	}
	close := strings.IndexByte(text[start:], ')')
	if close < 0 {
		writeEscapedByte(b, text[at])
		return at + 1
	}
	next := start + close + 1
	label := text[bracket+1 : bracket+end]
	url := strings.TrimSpace(text[start+1 : start+close])
	if !safeURL(url) {
		// A URL that could run code is written as the text it was: the export
		// leaves the machine, and "atlas wrote a working javascript: link into
		// the file" is not something to find out when it is opened.
		writeEscaped(b, text[at:next])
		return next
	}
	class := ""
	if image {
		class = ` class="image"`
	}
	b.WriteString(`<a` + class + ` href="` + escapeHTML(url) + `" rel="noopener">`)
	if label == "" {
		label = url
	}
	writeInline(b, label)
	b.WriteString("</a>")
	return next
}

// safeURL reports whether a link target may be written into the document: http,
// https, mailto, or a path relative to the file. Anything else — javascript:,
// data:, vbscript:, file: — is refused.
func safeURL(url string) bool {
	lower := strings.ToLower(url)
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		if c == ':' {
			scheme := lower[:i]
			if strings.ContainsAny(scheme, "/.") {
				// "example.com:8080/x" is a host and a port, not a scheme.
				return true
			}
			switch scheme {
			case "http", "https", "mailto":
				return true
			}
			return false
		}
		if c >= 'a' && c <= 'z' {
			continue
		}
		if (c >= '0' && c <= '9') || c == '+' || c == '.' || c == '-' || c == '/' {
			continue
		}
		return true
	}
	return true
}

// escapeHTML renders text for an attribute or a text node.
func escapeHTML(text string) string {
	var b strings.Builder
	writeEscaped(&b, text)
	return b.String()
}

func writeEscaped(b *strings.Builder, text string) {
	for i := 0; i < len(text); i++ {
		writeEscapedByte(b, text[i])
	}
}

func writeEscapedByte(b *strings.Builder, c byte) {
	switch c {
	case '&':
		b.WriteString("&amp;")
	case '<':
		b.WriteString("&lt;")
	case '>':
		b.WriteString("&gt;")
	case '"':
		b.WriteString("&quot;")
	case '\'':
		b.WriteString("&#39;")
	default:
		b.WriteByte(c)
	}
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// isMarkdownPunctuation is the set a backslash can escape.
func isMarkdownPunctuation(c byte) bool {
	return strings.IndexByte("\\`*_{}[]()#+-.!|~>\"'$%&,:;<=?@^/", c) >= 0
}

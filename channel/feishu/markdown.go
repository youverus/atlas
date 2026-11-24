package feishu

import (
	"regexp"
	"strings"
)

// Feishu text messages render a fixed, narrow subset of markdown: no emphasis,
// no links, no headers. The model answers in CommonMark anyway, so every
// outbound text passes through Downgrade: structure becomes the plain text
// that survives the bubble, and fenced code is left verbatim because its
// value IS the characters.
func Downgrade(text string) string {
	var out strings.Builder
	rest := text
	for {
		open := strings.Index(rest, "```")
		if open < 0 {
			out.WriteString(downgradeInline(rest))
			return out.String()
		}
		out.WriteString(downgradeInline(rest[:open]))
		// A fenced block passes through whole: the characters inside are the
		// value, and a plain-text bubble renders the fence fine.
		close := strings.Index(rest[open+3:], "```")
		if close < 0 {
			out.WriteString(rest[open:])
			return out.String()
		}
		end := open + 3 + close + 3
		out.WriteString(rest[open:end])
		rest = rest[end:]
	}
}

var (
	reImage  = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)[^)]*\)`)
	reLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)[^)]*\)`)
	reCode   = regexp.MustCompile("`([^`\n]+)`")
	reBold   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reDunder = regexp.MustCompile(`__([^_\n]+)__`)
	reItalic = regexp.MustCompile(`\*([^*\n]+)\*`)
	// Go's RE2 has no lookaround, so a single-word emphasis like _bar_ is
	// matched through its surroundings instead: `_` must start at a boundary
	// and end at one, which keeps foo_bar_baz whole.
	reUnderscore = regexp.MustCompile(`(^|[[:space:]({[])_([^_\n]+)_([[:space:])\].,!?;:]|$)`)
	reHeading    = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	reQuote      = regexp.MustCompile(`(?m)^[ \t]{0,3}>[ \t]?`)
	reAsterisk   = regexp.MustCompile(`(?m)^[ \t]{0,3}\*[ \t]+`)
)

// downgradeInline converts one markdown-safe stretch to plain text. Links keep
// their target — in a work chat, "见文档（https://...）" is what gets clicked
// from the notification shade. Inline code is protected before emphasis runs,
// so `a**b` survives as the characters the author wrote.
func downgradeInline(text string) string {
	var protected []string
	text = reCode.ReplaceAllStringFunc(text, func(match string) string {
		protected = append(protected, match[1:len(match)-1])
		return "\x00" + itoa(len(protected)-1) + "\x00"
	})

	text = reImage.ReplaceAllString(text, "$1")
	text = reLink.ReplaceAllString(text, "$1（$2）")
	text = reBold.ReplaceAllString(text, "$1")
	text = reDunder.ReplaceAllString(text, "$1")
	text = reItalic.ReplaceAllString(text, "$1")
	text = reUnderscore.ReplaceAllString(text, "$1$2$3")
	text = reHeading.ReplaceAllString(text, "")
	text = reQuote.ReplaceAllString(text, "")
	text = reAsterisk.ReplaceAllString(text, "• ")

	for i, raw := range protected {
		text = strings.ReplaceAll(text, "\x00"+itoa(i)+"\x00", raw)
	}
	return text
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

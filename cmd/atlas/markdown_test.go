package main

import (
	"strings"
	"testing"
)

// TestMarkdownRendersTheSubset pins what the HTML export understands. The
// reader is hand-written, so the list of things it knows is a decision rather
// than a property of a library: each of these is one shape an answer actually
// arrives in.
func TestMarkdownRendersTheSubset(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		want        string
		notExpected []string
	}{
		{
			name: "paragraph",
			in:   "one\ntwo",
			want: "<p>one two</p>",
		},
		{
			name: "heading",
			in:   "## Plan\n\ntext",
			want: "<h2>Plan</h2>",
		},
		{
			name: "hashtag is not a heading",
			in:   "#hashtag",
			want: "<p>#hashtag</p>",
		},
		{
			name: "fenced code keeps its language",
			in:   "```go\nfmt.Println()\n```",
			want: `<pre><code class="language-go">fmt.Println()</code></pre>`,
		},
		{
			name: "fence inside a fence",
			in:   "````md\n```go\nx\n```\n````",
			want: `<pre><code class="language-md">` + "```go\nx\n```" + `</code></pre>`,
		},
		{
			name: "unclosed fence runs to the end",
			in:   "```\nhalf",
			want: "<pre><code>half</code></pre>",
		},
		{
			name: "table",
			in:   "| a | b |\n|---|---|\n| 1 | 2 |",
			want: "<table>",
		},
		{
			name: "table alignment",
			in:   "| a | b |\n|:--|--:|\n| 1 | 2 |",
			want: `<td class="right">2</td>`,
		},
		{
			name: "a delimiter row without a header is text",
			in:   "|---|---|",
			want: "<p>",
			notExpected: []string{
				"<table>",
			},
		},
		{
			name: "unordered list",
			in:   "- one\n- two",
			want: "<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
		},
		{
			name: "ordered list keeps its start",
			in:   "3. three\n4. four",
			want: `<ol start="3">`,
		},
		{
			name: "nested list",
			in:   "- one\n  - two",
			want: "<li>\n<p>one</p>\n<ul>\n<li>two</li>\n</ul>\n</li>",
		},
		{
			name: "quote",
			in:   "> quoted",
			want: "<blockquote>\n<p>quoted</p>\n</blockquote>",
		},
		{
			name: "rule",
			in:   "a\n\n- - -\n\nb",
			want: "<hr>",
		},
		{
			name: "inline code",
			in:   "run `atlas -tui` now",
			want: "<code>atlas -tui</code>",
		},
		{
			name: "bold and italic",
			in:   "**bold** and *italic*",
			want: "<strong>bold</strong> and <em>italic</em>",
		},
		{
			name: "strikethrough",
			in:   "~~gone~~",
			want: "<del>gone</del>",
		},
		{
			name: "underscores inside a word stay",
			in:   "call Build_Context_Entries",
			want: "Build_Context_Entries",
			notExpected: []string{
				"<em>",
			},
		},
		{
			name: "asterisks that cannot pair stay",
			in:   "2 * 3 * 4",
			want: "<p>2 * 3 * 4</p>",
		},
		{
			name: "link",
			in:   "[docs](https://example.com/a?b=1&c=2)",
			want: `<a href="https://example.com/a?b=1&amp;c=2" rel="noopener">docs</a>`,
		},
		{
			name: "javascript link is text",
			in:   "[click](javascript:alert(1))",
			want: "[click](javascript:alert(1))",
			notExpected: []string{
				"<a ",
			},
		},
		{
			name: "image is a link",
			in:   "![shot](/tmp/a.png)",
			want: `<a class="image" href="/tmp/a.png" rel="noopener">shot</a>`,
			notExpected: []string{
				"<img",
			},
		},
		{
			name: "angle brackets are escaped",
			in:   "if a < b && c > d",
			want: "a &lt; b &amp;&amp; c &gt; d",
		},
		{
			name: "html in the answer is shown, not run",
			in:   "<script>alert(1)</script>",
			want: "&lt;script&gt;",
			notExpected: []string{
				"<script>",
			},
		},
		{
			name: "backslash escapes a marker",
			in:   `\*not italic\*`,
			want: "*not italic*",
			notExpected: []string{
				"<em>",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &strings.Builder{}
			writeMarkdown(b, c.in)
			text := b.String()
			if !strings.Contains(text, c.want) {
				t.Fatalf("render of %q = %q, want it to contain %q", c.in, text, c.want)
			}
			for _, unwanted := range c.notExpected {
				if strings.Contains(text, unwanted) {
					t.Fatalf("render of %q = %q, want no %q", c.in, text, unwanted)
				}
			}
		})
	}
}

// A table row with the wrong number of cells is fitted to the header instead of
// shifting the columns it would otherwise move: a ragged paste still lines up.
func TestMarkdownFitsRaggedTableRows(t *testing.T) {
	b := &strings.Builder{}
	writeMarkdown(b, "| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |")
	text := b.String()
	for _, want := range []string{"<td>1</td><td></td>", "<td>1</td><td>2</td>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("table = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "<td>3</td>") {
		t.Fatalf("table = %q, want the extra cell dropped", text)
	}
}

// The language word lands in an attribute, so a fence cannot smuggle one in.
func TestMarkdownSanitizesTheFenceLanguage(t *testing.T) {
	b := &strings.Builder{}
	writeMarkdown(b, "```go\" onmouseover=\"x\ny\n```")
	text := b.String()
	if strings.Contains(text, "onmouseover") {
		t.Fatalf("render = %q, want the attribute characters dropped", text)
	}
	if !strings.Contains(text, `class="language-go"`) {
		t.Fatalf("render = %q, want the language kept", text)
	}
}

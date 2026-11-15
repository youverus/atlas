package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pi-go/internal/wire"
)

// htmlExport renders messages with a fixed header, the way the Markdown tests
// render a document: the shape is a contract, so it is asserted on the bytes.
func htmlExport(t *testing.T, messages []wire.Message) string {
	t.Helper()
	got := &bytes.Buffer{}
	header := exportHeader{
		name:     "review",
		session:  "01a16ec2-2818-7101",
		cwd:      "/tmp/project",
		model:    "test/m1",
		exported: time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC),
	}
	if err := exportHTML(got, header, messages); err != nil {
		t.Fatalf("export: %v", err)
	}
	return got.String()
}

// TestExportHTMLRendersTablesAndCode is the completion criterion of the work
// item: an answer that holds a table and a fenced block has to come out as a
// table and a code block, not as the Markdown it was written in.
func TestExportHTMLRendersTablesAndCode(t *testing.T) {
	answer := "| file | lines |\n|:--|--:|\n| main.go | 120 |\n\n```go\nfunc main() {}\n```"
	text := htmlExport(t, []wire.Message{{
		Role:       wire.RoleAssistant,
		Content:    wire.NewBlocks(wire.TextContent(answer)),
		StopReason: wire.StopStop,
	}})

	for _, want := range []string{
		"<table>",
		"<th>file</th>",
		`<td class="right">120</td>`,
		`<pre><code class="language-go">func main() {}</code></pre>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("document = %q, want %q", text, want)
		}
	}
	// The source itself must be gone: a document that shows "| file | lines |"
	// next to a rendered table has printed the input as well as the output.
	for _, unwanted := range []string{"| file | lines |", "```go", "|:--|--:|"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("document = %q, want no raw %q", text, unwanted)
		}
	}
}

// A whole turn reaches the file: thinking folded away, the answer, the call, the
// result in its own section, and the reason an answer stopped.
func TestExportHTMLCarriesTheWholeTurn(t *testing.T) {
	text := htmlExport(t, []wire.Message{
		wire.SystemMessage("you are an agent"),
		wire.UserMessage("what is here"),
		{
			Role: wire.RoleAssistant,
			Content: wire.NewBlocks(
				wire.ThinkingContent("look first", ""),
				wire.TextContent("one file"),
				wire.ToolCallContent("call_1", "bash", json.RawMessage(`{"command":"ls"}`)),
			),
			StopReason: wire.StopToolUse,
		},
		{
			Role:       wire.RoleToolResult,
			Content:    wire.NewBlocks(wire.TextContent("a.txt")),
			ToolName:   "bash",
			ToolCallID: "call_1",
			IsError:    wire.Ptr(true),
		},
		{
			Role:         wire.RoleAssistant,
			Content:      wire.NewBlocks(wire.TextContent("done")),
			StopReason:   wire.StopError,
			ErrorMessage: "rate limited",
		},
	})

	for _, want := range []string{
		"<h1>review</h1>",
		"<dt>cwd</dt><dd>/tmp/project</dd>",
		`<details class="thinking">`,
		"<summary>Thinking</summary>",
		"<p>look first</p>",
		`<p class="call-name">tool call: bash</p>`,
		"&quot;command&quot;: &quot;ls&quot;",
		"<h2>Tool result: bash (error)</h2>",
		"<pre><code>a.txt</code></pre>",
		`<p class="error">error: rate limited</p>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("document = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "you are an agent") {
		t.Fatal("the system message was exported")
	}
}

// The document is self-contained: no script, no stylesheet or font fetched from
// somewhere, so it renders the same on a machine with no network.
func TestExportHTMLIsSelfContained(t *testing.T) {
	text := htmlExport(t, []wire.Message{wire.UserMessage("hi")})
	for _, unwanted := range []string{"<script", "<link", "@import", "src=\"http"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("document = %q, want no %q", text, unwanted)
		}
	}
	if !strings.Contains(text, "<style>") {
		t.Fatalf("document = %q, want its stylesheet inline", text)
	}
	if !strings.Contains(text, "<footer class=\"session\"><p>atlas "+version+"</p></footer>") {
		t.Fatalf("document = %q, want the version in the footer", text)
	}
}

// Tool output is evidence, not prose: it is escaped into a code block, so an
// answer that read a file full of angle brackets cannot rewrite the page.
func TestExportHTMLEscapesToolOutput(t *testing.T) {
	text := htmlExport(t, []wire.Message{{
		Role:     wire.RoleToolResult,
		Content:  wire.NewBlocks(wire.TextContent("<script>alert(1)</script>")),
		ToolName: "read",
		IsError:  wire.Ptr(false),
	}})
	if strings.Contains(text, "<script>") {
		t.Fatalf("document = %q, want the script escaped", text)
	}
	if !strings.Contains(text, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("document = %q, want the escaped text", text)
	}
}

// An empty session still produces a document that says so, instead of a page
// with a title and nothing under it.
func TestExportHTMLSaysAnEmptySessionIsEmpty(t *testing.T) {
	text := htmlExport(t, []wire.Message{wire.SystemMessage("you are an agent")})
	if !strings.Contains(text, "This session holds no messages.") {
		t.Fatalf("document = %q, want the empty note", text)
	}
}

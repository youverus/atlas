package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const (
	// mcpTextLimit is how much text one result may put in front of the model.
	// It matches pi: past this, the middle is cut out and the whole result goes
	// to a file, because a single tool that returns a megabyte of prose costs
	// the rest of the conversation.
	mcpTextLimit = 20 << 10

	// emptySchema is what a server that declared no input schema gets. Every
	// provider expects an object schema, and "no arguments" is exactly that.
	emptySchema = `{"type":"object","properties":{}}`
)

// RemoteTool is one tool of one server as this session knows it.
type RemoteTool struct {
	// Server is the configured server name.
	Server string
	// Name is the name the server gave the tool, used in tools/call.
	Name string
	// WireName is what the model sees: mcp__server__tool. See assignNames.
	WireName string
	// Title, Description and Schema are passed through unchanged. The model
	// reads them, so paraphrasing would only lose information the server
	// intended to give.
	Title       string
	Description string
	Schema      json.RawMessage
}

// toolProxy adapts one remote tool to tool.Tool.
//
// The wrapper is thin on purpose: everything it could add — a shorter
// description, a flattened schema — is something the server already said better.
type toolProxy struct {
	client *client
	remote RemoteTool
	// spill is where a result too large for a message lands. Nil means the OS
	// temp directory, which is where it went before the set carried one.
	spill *tool.Spill
}

// Name is the model-facing identifier.
func (p *toolProxy) Name() string { return p.remote.WireName }

// Label is the server and the tool as the server spelled them. The interface is
// read by a person, and "mcp__fs__read_file" is the machine's spelling.
func (p *toolProxy) Label() string { return p.remote.Server + "/" + p.remote.Name }

// Description is the server's description. A tool without one is described by
// where it came from, which is at least actionable.
func (p *toolProxy) Description() string {
	if p.remote.Description != "" {
		return p.remote.Description
	}
	return fmt.Sprintf("tool %q on the MCP server %q", p.remote.Name, p.remote.Server)
}

// Parameters is the server's input schema, unchanged.
func (p *toolProxy) Parameters() json.RawMessage {
	if len(p.remote.Schema) == 0 {
		return json.RawMessage(emptySchema)
	}
	return p.remote.Schema
}

// Execute runs the call on the server.
//
// onUpdate receives one line per progress notification. That is not a preview of
// the result — a tools/call answer arrives whole — it is the server saying how
// far along it is, which also keeps the call's idle budget from running out
// (docs/mcp.md section 4).
//
// A transport failure is returned as an error and a failure the server reported
// comes back as Result.IsError. The two are kept apart because they mean
// different things: the first is "nothing came back", the second is "the tool
// ran and failed", and a model that cannot tell them apart retries the wrong one.
func (p *toolProxy) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	answer, err := p.client.callTool(ctx, p.remote.Name, call.Arguments, onUpdate)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", p.remote.WireName, err)
	}
	return renderResult(p.remote, answer, p.spill), nil
}

// renderResult turns a tools/call answer into what the model reads.
//
// Text blocks are accumulated in order and the rest are appended after them. That
// loses the interleaving between text and images, which costs nothing here: the
// providers send a message's blocks in order anyway, and a result whose point is
// "here is an image" does not depend on the sentence being first.
func renderResult(remote RemoteTool, answer callResult, spill *tool.Spill) tool.Result {
	var texts []string
	var extras []wire.Content
	for _, block := range answer.Content {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "image":
			if block.Data == "" {
				texts = append(texts, fmt.Sprintf("[%s sent an image with no data]", remote.Server))
				continue
			}
			// Base64 is kept as it came: wire carries images encoded, and
			// decoding only to re-encode it would be work with no reader.
			extras = append(extras, wire.ImageContent(block.Data, block.MIMEType))
		case "audio":
			texts = append(texts, spillBinary(remote, "audio", block.MIMEType, block.Data, spill))
		case "resource":
			texts = append(texts, renderResource(remote, block, spill))
		case "resource_link":
			texts = append(texts, renderLink(block))
		default:
			texts = append(texts, fmt.Sprintf("[%s sent content of an unsupported type %q]", remote.Server, block.Type))
		}
	}

	text := strings.Join(texts, "\n")
	if len(text) > mcpTextLimit {
		// Truncation happens whether or not the file could be written: the model
		// must not get an unbounded result just because the disk is full.
		full := text
		text = tool.TruncateMiddle(full, tool.Limits{MaxBytes: mcpTextLimit}).Content
		if path, err := spill.Write("atlas-mcp", "", []byte(full)); err == nil {
			text += fmt.Sprintf("\n[the full result (%s) was written to %s]", tool.FormatSize(len(full)), path)
		}
	}

	content := make([]wire.Content, 0, 1+len(extras))
	if text != "" {
		content = append(content, wire.TextContent(text))
	}
	content = append(content, extras...)
	if len(content) == 0 {
		content = append(content, wire.TextContent("[the tool returned no content]"))
	}
	return tool.Result{
		Content:           content,
		Details:           detailsOf(remote),
		StructuredContent: answer.StructuredContent,
		IsError:           answer.IsError,
	}
}

// renderResource renders an embedded resource. Text is inlined with its URI so
// the model knows where it came from; a blob is saved and named, because atlas
// has no tool that reads resources and pretending otherwise would be a lie the
// model acts on.
func renderResource(remote RemoteTool, block contentBlock, spill *tool.Spill) string {
	if block.Resource == nil {
		return fmt.Sprintf("[%s sent a resource block without content]", remote.Server)
	}
	embedded := block.Resource
	if embedded.Text != "" {
		header := embedded.URI
		if embedded.MIMEType != "" {
			header += " (" + embedded.MIMEType + ")"
		}
		return header + ":\n" + embedded.Text
	}
	if embedded.Blob != "" {
		return spillBinary(remote, "resource "+embedded.URI, embedded.MIMEType, embedded.Blob, spill)
	}
	return fmt.Sprintf("[resource %s was empty]", embedded.URI)
}

// renderLink names a resource the tool points at. Saying what atlas cannot do is
// part of the message: a model that thinks it can read the URI will try.
func renderLink(block contentBlock) string {
	name := block.Name
	if name == "" {
		name = block.URI
	}
	details := block.URI
	if block.MIMEType != "" {
		details += " (" + block.MIMEType + ")"
	}
	return fmt.Sprintf("[resource %s at %s; atlas has no tool that reads MCP resources]", name, details)
}

// spillBinary saves content that has no content type here and returns the line
// that stands in for it. wire carries text, thinking, tool calls and images;
// teaching three providers a fifth block for audio would cost more than the
// audio is worth in a terminal.
func spillBinary(remote RemoteTool, kind, mimeType, data string, spill *tool.Spill) string {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Sprintf("[%s from %s could not be decoded: %v]", kind, remote.Server, err)
	}
	label := kind
	if mimeType != "" {
		label = kind + " " + mimeType
	}
	path, err := spill.Write("atlas-mcp", "", raw)
	if err != nil {
		return fmt.Sprintf("[%s (%s) from %s could not be saved: %v]", label, tool.FormatSize(len(raw)), remote.Server, err)
	}
	return fmt.Sprintf("[%s (%s) from %s saved to %s; atlas cannot show it inline]", label, tool.FormatSize(len(raw)), remote.Server, path)
}

// detailsOf records where a result came from. A session file that says only
// "mcp__fs__read_file" does not say which server answered, and the server is the
// part that can change under the same tool name.
func detailsOf(remote RemoteTool) json.RawMessage {
	encoded, err := json.Marshal(struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}{Server: remote.Server, Tool: remote.Name})
	if err != nil {
		return nil
	}
	return encoded
}

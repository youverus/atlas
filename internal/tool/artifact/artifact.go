// Package artifact implements the agent tools for creating, updating,
// listing, and reading session-scoped artifacts.
package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"pi-go/internal/artifact"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// Options configures the artifact toolset for one workspace directory.
type Options struct {
	// Cwd is the workspace root used when resolving optional source_path reads.
	Cwd string
	// DefaultStore is used when ctx carries no SessionContext and ResolveStore is nil.
	DefaultStore *artifact.Store
	// ResolveStore optionally resolves the session-scoped Store for ctx.
	ResolveStore func(ctx context.Context) (*artifact.Store, error)
}

type shared struct {
	cwd          string
	defaultStore *artifact.Store
	resolveStore func(ctx context.Context) (*artifact.Store, error)
}

// Tools returns the four artifact tools in registration order.
func Tools(options Options) []tool.Tool {
	s := &shared{
		cwd:          options.Cwd,
		defaultStore: options.DefaultStore,
		resolveStore: options.ResolveStore,
	}
	return []tool.Tool{
		publishTool{s: s},
		patchTool{s: s},
		listTool{s: s},
		readTool{s: s},
	}
}

func (s *shared) store(ctx context.Context) (*artifact.Store, error) {
	if s.resolveStore != nil {
		return s.resolveStore(ctx)
	}
	if s.defaultStore != nil {
		return s.defaultStore, nil
	}
	return nil, errors.New("artifact: store is unavailable")
}

func (s *shared) effectiveCwd(ctx context.Context) string {
	if sess, ok := tool.SessionFromContext(ctx); ok && strings.TrimSpace(sess.Cwd) != "" {
		return sess.Cwd
	}
	return s.cwd
}

func decodeStrict(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func artifactRefResult(detail artifact.Detail, action, text string) tool.Result {
	var summary string
	var sourcePath string
	if len(detail.Versions) > 0 {
		last := detail.Versions[len(detail.Versions)-1]
		summary = last.Summary
		sourcePath = last.SourcePath
	}
	ref := wire.ArtifactRef{
		ID:          detail.ID,
		Kind:        detail.Kind,
		Title:       detail.Title,
		Description: detail.Description,
		Language:    detail.Language,
		ContentType: detail.ContentType,
		Version:     detail.Version,
		Bytes:       detail.Bytes,
		SHA256:      detail.SHA256,
		Action:      action,
		Summary:     summary,
		SourcePath:  sourcePath,
	}
	raw, _ := json.Marshal(ref)
	return tool.Result{
		Content: []wire.Content{wire.TextContent(text)},
		Details: raw,
	}
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.2f MB", float64(n)/(1024*1024))
}

// --- artifact_publish ---

const publishDescription = "Create or publish a new version of a user-facing artifact (interactive HTML/CSS/JS visualization or app, SVG diagram, Markdown report, CSV table, or standalone code) displayed in the Atlas Artifact viewer without modifying project workspace files. Supply either content or source_path (relative to the workspace). For incremental updates to an existing artifact, prefer artifact_patch."

var publishParams = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id": map[string]any{
			"type":        "string",
			"description": "Lowercase slug identifier (1-64 chars, ^[a-z0-9][a-z0-9_-]*$), e.g. 'sales-dashboard' or 'arch-diagram'",
		},
		"kind": map[string]any{
			"type":        "string",
			"enum":        []string{artifact.KindHTML, artifact.KindSVG, artifact.KindMarkdown, artifact.KindCode, artifact.KindCSV},
			"description": "Artifact format: html for interactive visualizations/pages, svg for vector diagrams, markdown for formatted reports, csv for tables, code for standalone code",
		},
		"title": map[string]any{
			"type":        "string",
			"description": "Human-readable title shown in the artifact card and viewer header",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "Concise one-line description of the artifact",
		},
		"language": map[string]any{
			"type":        "string",
			"description": "Language hint when kind='code' (e.g. 'typescript', 'python', 'go', 'json')",
		},
		"content": map[string]any{
			"type":        "string",
			"description": "Full UTF-8 content of the artifact (required unless source_path is provided)",
		},
		"source_path": map[string]any{
			"type":        "string",
			"description": "Workspace-relative file path to snapshot into the artifact instead of passing content inline",
		},
		"summary": map[string]any{
			"type":        "string",
			"description": "Brief note describing this version",
		},
	},
	"required":             []string{"id", "kind"},
	"additionalProperties": false,
})

type publishArgs struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Content     string `json:"content"`
	SourcePath  string `json:"source_path"`
	Summary     string `json:"summary"`
}

type publishTool struct{ s *shared }

func (t publishTool) Name() string                { return wire.ArtifactPublishToolName }
func (t publishTool) Label() string               { return "artifact_publish" }
func (t publishTool) Description() string         { return publishDescription }
func (t publishTool) Parameters() json.RawMessage { return publishParams }

func (t publishTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var args publishArgs
	if err := decodeStrict(call.Arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("artifact_publish: invalid arguments: %w", err)
	}
	hasContent := args.Content != ""
	hasSource := strings.TrimSpace(args.SourcePath) != ""
	if hasContent && hasSource {
		return tool.Result{}, errors.New("artifact_publish: pass either content or source_path, not both")
	}
	if !hasContent && !hasSource {
		return tool.Result{}, errors.New("artifact_publish: content or source_path is required")
	}

	content := args.Content
	sourceRel := ""
	if hasSource {
		var err error
		content, sourceRel, err = readConfinedSource(t.s.effectiveCwd(ctx), args.SourcePath)
		if err != nil {
			return tool.Result{}, fmt.Errorf("artifact_publish: %w", err)
		}
	}

	store, err := t.s.store(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	detail, err := store.Publish(artifact.PublishRequest{
		ID:          args.ID,
		Kind:        args.Kind,
		Title:       args.Title,
		Description: args.Description,
		Language:    args.Language,
		Content:     content,
		SourcePath:  sourceRel,
		Summary:     args.Summary,
		ToolCallID:  call.ID,
	})
	if err != nil {
		return tool.Result{}, err
	}
	text := fmt.Sprintf("Published artifact %q (%s, v%d, %s): %s", detail.ID, detail.Kind, detail.Version, humanBytes(detail.Bytes), detail.Title)
	return artifactRefResult(detail, "publish", text), nil
}

// --- artifact_patch ---

const patchDescription = "Update an existing artifact incrementally by applying exact string replacements to its latest version, producing a new immutable version (v2, v3, ...) without re-sending the entire content."

var patchParams = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id": map[string]any{
			"type":        "string",
			"description": "Existing artifact ID to update",
		},
		"edits": map[string]any{
			"type":        "array",
			"description": "Ordered exact text replacements applied sequentially to the latest version",
			"minItems":    1,
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"old_text":    map[string]any{"type": "string", "description": "Exact existing text to replace"},
					"new_text":    map[string]any{"type": "string", "description": "Replacement text"},
					"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match"},
				},
				"required":             []string{"old_text", "new_text"},
				"additionalProperties": false,
			},
		},
		"title": map[string]any{
			"type":        "string",
			"description": "Optional updated display title",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "Optional updated description",
		},
		"summary": map[string]any{
			"type":        "string",
			"description": "Brief note describing what changed in this version",
		},
	},
	"required":             []string{"id", "edits"},
	"additionalProperties": false,
})

type patchArgs struct {
	ID          string          `json:"id"`
	Edits       []artifact.Edit `json:"edits"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Summary     string          `json:"summary"`
}

type patchTool struct{ s *shared }

func (t patchTool) Name() string                { return wire.ArtifactPatchToolName }
func (t patchTool) Label() string               { return "artifact_patch" }
func (t patchTool) Description() string         { return patchDescription }
func (t patchTool) Parameters() json.RawMessage { return patchParams }

func (t patchTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var args patchArgs
	if err := decodeStrict(call.Arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("artifact_patch: invalid arguments: %w", err)
	}
	store, err := t.s.store(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	detail, err := store.Patch(artifact.PatchRequest{
		ID:          args.ID,
		Title:       args.Title,
		Description: args.Description,
		Summary:     args.Summary,
		ToolCallID:  call.ID,
		Edits:       args.Edits,
	})
	if err != nil {
		return tool.Result{}, err
	}
	text := fmt.Sprintf("Updated artifact %q -> v%d (%d edit(s), %s): %s", detail.ID, detail.Version, len(args.Edits), humanBytes(detail.Bytes), detail.Title)
	return artifactRefResult(detail, "patch", text), nil
}

// --- artifact_list ---

var emptyParams = mustJSON(map[string]any{
	"type":                 "object",
	"properties":           map[string]any{},
	"additionalProperties": false,
})

type listTool struct{ s *shared }

func (t listTool) Name() string  { return wire.ArtifactListToolName }
func (t listTool) Label() string { return "artifact_list" }
func (t listTool) Description() string {
	return "List all artifacts published in the current conversation with their IDs, kinds, titles, latest version numbers, and byte sizes."
}
func (t listTool) Parameters() json.RawMessage { return emptyParams }

func (t listTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var args struct{}
	if err := decodeStrict(call.Arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("artifact_list: invalid arguments: %w", err)
	}
	store, err := t.s.store(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	items, err := store.List()
	if err != nil {
		return tool.Result{}, err
	}
	if len(items) == 0 {
		return tool.Result{Content: []wire.Content{wire.TextContent("No artifacts in this conversation.")}}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d artifact(s):\n", len(items))
	for _, item := range items {
		fmt.Fprintf(&b, "- %s (%s, v%d, %s): %s", item.ID, item.Kind, item.Version, humanBytes(item.Bytes), item.Title)
		if item.Language != "" {
			fmt.Fprintf(&b, " [%s]", item.Language)
		}
		if item.Description != "" {
			fmt.Fprintf(&b, " — %s", item.Description)
		}
		b.WriteByte('\n')
	}
	return tool.Result{Content: []wire.Content{wire.TextContent(strings.TrimRight(b.String(), "\n"))}}, nil
}

// --- artifact_read ---

var readParams = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id":      map[string]any{"type": "string", "description": "Artifact ID to read"},
		"version": map[string]any{"type": "integer", "description": "Optional version number (defaults to latest)"},
		"offset":  map[string]any{"type": "integer", "description": "Optional 1-based starting line number"},
		"limit":   map[string]any{"type": "integer", "description": "Optional maximum number of lines to return"},
	},
	"required":             []string{"id"},
	"additionalProperties": false,
})

type readArgs struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
}

type readTool struct{ s *shared }

func (t readTool) Name() string  { return wire.ArtifactReadToolName }
func (t readTool) Label() string { return "artifact_read" }
func (t readTool) Description() string {
	return "Read an artifact's metadata, version count, and content (optionally by version or line range)."
}
func (t readTool) Parameters() json.RawMessage { return readParams }

func (t readTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var args readArgs
	if err := decodeStrict(call.Arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("artifact_read: invalid arguments: %w", err)
	}
	store, err := t.s.store(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	detail, err := store.Get(args.ID, args.Version)
	if err != nil {
		return tool.Result{}, err
	}
	body := sliceLines(detail.Content, args.Offset, args.Limit)
	var b strings.Builder
	fmt.Fprintf(&b, "Artifact %q (%s, showing v%d of v%d, %s): %s\n", detail.ID, detail.Kind, detail.ActiveVersion, detail.Version, humanBytes(detail.Bytes), detail.Title)
	if detail.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", detail.Description)
	}
	b.WriteString("---\n")
	b.WriteString(body)
	return tool.Result{Content: []wire.Content{wire.TextContent(b.String())}}, nil
}

func sliceLines(text string, offset, limit int) string {
	if offset <= 1 && limit <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	start := 0
	if offset > 1 {
		start = offset - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

func readConfinedSource(cwd, rawPath string) (string, string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", "", errors.New("source_path must not be empty")
	}
	if filepath.IsAbs(trimmed) {
		return "", "", fmt.Errorf("source_path %q must be relative to the workspace", rawPath)
	}
	if strings.TrimSpace(cwd) == "" {
		return "", "", errors.New("workspace directory is unavailable for source_path")
	}
	absRoot, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace %s: %w", absRoot, err)
	}
	cleanRel := filepath.Clean(filepath.FromSlash(trimmed))
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("source_path %q escapes workspace", rawPath)
	}
	candidate := filepath.Join(realRoot, cleanRel)
	realTarget, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("read source_path %q: %w", rawPath, err)
	}
	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("source_path %q resolves outside workspace", rawPath)
	}
	info, err := os.Stat(realTarget)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("source_path %q is a directory", rawPath)
	}
	if info.Size() > artifact.MaxContentBytes {
		return "", "", fmt.Errorf("source_path %q is %d bytes, limit is %d bytes", rawPath, info.Size(), artifact.MaxContentBytes)
	}
	raw, err := os.ReadFile(realTarget)
	if err != nil {
		return "", "", err
	}
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		return "", "", fmt.Errorf("source_path %q must be a valid UTF-8 text file", rawPath)
	}
	return string(raw), filepath.ToSlash(cleanRel), nil
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

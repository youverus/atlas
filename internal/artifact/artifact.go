// Package artifact stores versioned, session-scoped artifacts (interactive HTML,
// SVG diagrams, Markdown reports, CSV tables, and standalone code) using only
// the Go standard library.
package artifact

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// KindHTML represents an interactive HTML document or visualization fragment.
	KindHTML = "html"
	// KindSVG represents a scalable vector graphic.
	KindSVG = "svg"
	// KindMarkdown represents a formatted Markdown document.
	KindMarkdown = "markdown"
	// KindCode represents standalone source code, configuration, or JSON.
	KindCode = "code"
	// KindCSV represents comma-separated tabular data.
	KindCSV = "csv"
)

const (
	// MaxContentBytes caps a single artifact version at 2 MiB so previews and
	// HTTP payloads remain bounded.
	MaxContentBytes = 2 << 20
	// MaxVersions caps version history per artifact ID.
	MaxVersions = 64
	// MaxArtifacts caps distinct artifacts per session.
	MaxArtifacts = 100
	// MaxIDLength caps artifact slug length.
	MaxIDLength = 64
	// MaxTitleRunes caps display title length.
	MaxTitleRunes = 160
	// MaxDescriptionRunes caps one-line description length.
	MaxDescriptionRunes = 500
	// MaxSummaryRunes caps per-version change summary length.
	MaxSummaryRunes = 240
	// MaxLanguageLength caps language identifier length.
	MaxLanguageLength = 48
	// MaxEdits caps the number of edits in a single patch call.
	MaxEdits = 50
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidateID checks whether id is a valid lowercase artifact slug.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("artifact: id is required")
	}
	if len(id) > MaxIDLength {
		return fmt.Errorf("artifact: id %q exceeds %d characters", id, MaxIDLength)
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("artifact: id %q must match ^[a-z0-9][a-z0-9_-]*$", id)
	}
	return nil
}

// ValidKind reports whether kind is one of the supported artifact kinds.
func ValidKind(kind string) bool {
	switch kind {
	case KindHTML, KindSVG, KindMarkdown, KindCode, KindCSV:
		return true
	}
	return false
}

// ContentTypeFor returns the canonical MIME type for an artifact kind and
// optional language hint.
func ContentTypeFor(kind, language string) string {
	switch kind {
	case KindHTML:
		return "text/html; charset=utf-8"
	case KindSVG:
		return "image/svg+xml"
	case KindMarkdown:
		return "text/markdown; charset=utf-8"
	case KindCSV:
		return "text/csv; charset=utf-8"
	case KindCode:
		switch strings.ToLower(strings.TrimSpace(language)) {
		case "json":
			return "application/json; charset=utf-8"
		case "css":
			return "text/css; charset=utf-8"
		case "js", "javascript":
			return "text/javascript; charset=utf-8"
		default:
			return "text/plain; charset=utf-8"
		}
	default:
		return "text/plain; charset=utf-8"
	}
}

// DefaultExtension returns a clean file extension (without leading dot) used
// when suggesting export or download filenames.
func DefaultExtension(kind, language string) string {
	switch kind {
	case KindHTML:
		return "html"
	case KindSVG:
		return "svg"
	case KindMarkdown:
		return "md"
	case KindCSV:
		return "csv"
	case KindCode:
		switch strings.ToLower(strings.TrimSpace(language)) {
		case "typescript", "ts":
			return "ts"
		case "tsx":
			return "tsx"
		case "javascript", "js":
			return "js"
		case "jsx":
			return "jsx"
		case "python", "py":
			return "py"
		case "go", "golang":
			return "go"
		case "rust", "rs":
			return "rs"
		case "json":
			return "json"
		case "yaml", "yml":
			return "yaml"
		case "sql":
			return "sql"
		case "css":
			return "css"
		case "sh", "bash", "zsh", "shell":
			return "sh"
		default:
			return "txt"
		}
	default:
		return "txt"
	}
}

// Summary holds the latest metadata for one artifact in a session.
type Summary struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	ContentType string `json:"contentType"`
	Version     int    `json:"version"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// VersionInfo describes one immutable published snapshot of an artifact.
type VersionInfo struct {
	Version     int    `json:"version"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`
	Language    string `json:"language,omitempty"`
	ContentType string `json:"contentType"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	SourcePath  string `json:"sourcePath,omitempty"`
	ToolCallID  string `json:"toolCallId,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	Action      string `json:"action,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// Detail carries an artifact's full metadata, version history, and the
// requested version's content.
type Detail struct {
	Summary
	ActiveVersion int           `json:"activeVersion"`
	Content       string        `json:"content"`
	Versions      []VersionInfo `json:"versions"`
}

// PublishRequest creates a new artifact or publishes a new full version of an
// existing artifact.
type PublishRequest struct {
	ID          string
	Kind        string
	Title       string
	Description string
	Language    string
	Content     string
	SourcePath  string
	Summary     string
	ToolCallID  string
}

// Edit represents one exact string replacement in a PatchRequest.
type Edit struct {
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// PatchRequest applies sequential edits to the latest version of an existing
// artifact, producing a new immutable version.
type PatchRequest struct {
	ID          string
	Title       string
	Description string
	Summary     string
	ToolCallID  string
	Edits       []Edit
}

func normalizeTextField(value string, maxRunes int, field string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if !utf8.ValidString(trimmed) {
		return "", fmt.Errorf("artifact: %s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return "", fmt.Errorf("artifact: %s exceeds %d characters", field, maxRunes)
	}
	return trimmed, nil
}

func validateContent(content string) error {
	if len(content) == 0 {
		return fmt.Errorf("artifact: content must not be empty")
	}
	if len(content) > MaxContentBytes {
		return fmt.Errorf("artifact: content is %d bytes, limit is %d bytes", len(content), MaxContentBytes)
	}
	if !utf8.ValidString(content) {
		return fmt.Errorf("artifact: content must be valid UTF-8")
	}
	if strings.IndexByte(content, 0) >= 0 {
		return fmt.Errorf("artifact: content must not contain NUL bytes")
	}
	return nil
}

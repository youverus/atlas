package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const grepDescription = `Search file contents for a pattern. Returns matching lines with file paths and line numbers. ` +
	`Respects .gitignore. Output is truncated to 100 matches or 50KB (whichever is hit first). ` +
	`Long lines are truncated to 500 chars.`

const grepParameters = `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Search pattern (regex or literal string)"},
    "path": {"type": "string", "description": "Directory or file to search (default: current directory)"},
    "glob": {"type": "string", "description": "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"},
    "ignoreCase": {"type": "boolean", "description": "Case-insensitive search (default: false)"},
    "literal": {"type": "boolean", "description": "Treat pattern as literal string instead of regex (default: false)"},
    "context": {"type": "number", "description": "Number of lines to show before and after each match (default: 0)"},
    "limit": {"type": "number", "description": "Maximum number of matches to return (default: 100)"}
  },
  "required": ["pattern"]
}`

const grepDefaultLimit = 100

type grepTool struct{ set *shared }

func (t grepTool) Name() string                { return "grep" }
func (t grepTool) Label() string               { return "grep" }
func (t grepTool) Description() string         { return grepDescription }
func (t grepTool) Parameters() json.RawMessage { return json.RawMessage(grepParameters) }

type grepRequest struct {
	Pattern    string       `json:"pattern"`
	Path       string       `json:"path"`
	Glob       string       `json:"glob"`
	IgnoreCase bool         `json:"ignoreCase"`
	Literal    bool         `json:"literal"`
	Context    tool.Number  `json:"context"`
	Limit      *tool.Number `json:"limit"`
}

type grepDetails struct {
	Truncation        *tool.Truncation `json:"truncation,omitempty"`
	MatchLimitReached int              `json:"matchLimitReached,omitempty"`
	LinesTruncated    bool             `json:"linesTruncated,omitempty"`
}

// grepMatch is one matching line, kept as a file and a line number so context
// lines can be read after the walk finishes.
type grepMatch struct {
	rel  string
	line int
}

func (t grepTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request grepRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if request.Pattern == "" {
		return tool.Result{}, fmt.Errorf("grep: pattern is required")
	}

	searchPath := "."
	if request.Path != "" {
		searchPath = request.Path
	}
	searchRoot, err := t.set.resolveAccess(searchPath)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := t.set.stat(searchRoot)
	if err != nil {
		if isNotExist(err) {
			return tool.Result{}, fmt.Errorf("Path not found: %s", searchRoot)
		}
		return tool.Result{}, err
	}
	isDirectory := info.IsDir()

	matcher, err := compileGrepPattern(request.Pattern, request.IgnoreCase, request.Literal)
	if err != nil {
		return tool.Result{}, err
	}

	contextLines := max(0, request.Context.Int())
	limit := grepDefaultLimit
	if request.Limit != nil {
		limit = max(1, request.Limit.Int())
	}

	var matches []grepMatch
	limitReached := false
	linesTruncated := false

	handleFile := func(rel, full string) (bool, error) {
		content, readErr := t.set.readFile(full)
		if readErr != nil {
			// A file that vanished or is unreadable is skipped, the way rg does.
			return true, nil
		}
		if looksBinary(content) {
			return true, nil
		}
		lines := strings.Split(normalizeToLF(string(content)), "\n")
		for i, line := range lines {
			if !matcher.MatchString(line) {
				continue
			}
			if len(matches) >= limit {
				limitReached = true
				return false, nil
			}
			matches = append(matches, grepMatch{rel: rel, line: i + 1})
		}
		return true, nil
	}

	if isDirectory {
		checkGlob := func(rel string, isDir bool) bool {
			if isDir || request.Glob == "" {
				return true
			}
			return matchPattern(request.Glob, rel)
		}
		w := &walker{
			ctx:     ctx,
			set:     t.set,
			ignores: newIgnoreStack(),
			visit: func(entry walkEntry) (bool, error) {
				if !checkGlob(entry.Rel, entry.IsDir) {
					return true, nil
				}
				return handleFile(toSlash(entry.Rel), entry.Path)
			},
		}
		if err := w.walkTree(searchRoot); err != nil {
			return tool.Result{}, err
		}
	} else {
		if _, err := handleFile(filepath.Base(searchRoot), searchRoot); err != nil {
			return tool.Result{}, err
		}
	}
	if len(matches) >= limit {
		limitReached = true
	}

	if len(matches) == 0 {
		return tool.Result{
			Content: []wire.Content{wire.TextContent("No matches found")},
		}, nil
	}

	lineCache := make(map[string][]string)
	fileLines := func(rel string) []string {
		if cached, ok := lineCache[rel]; ok {
			return cached
		}
		full := searchRoot
		if isDirectory {
			full = joinAccessPath(searchRoot, filepath.FromSlash(rel))
		}
		content, readErr := t.set.readFile(full)
		if readErr != nil {
			lineCache[rel] = nil
			return nil
		}
		lines := strings.Split(normalizeToLF(string(content)), "\n")
		lineCache[rel] = lines
		return lines
	}

	outputLines := make([]string, 0, len(matches))
	for _, match := range matches {
		lines := fileLines(match.rel)
		if len(lines) == 0 {
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: (unable to read file)", match.rel, match.line))
			continue
		}
		start := match.line
		end := match.line
		if contextLines > 0 {
			start = max(1, match.line-contextLines)
			end = min(len(lines), match.line+contextLines)
		}
		for current := start; current <= end; current++ {
			text := ""
			if current-1 < len(lines) {
				text = lines[current-1]
			}
			// Strip stray carriage returns so terminal output stays on one line.
			text = strings.ReplaceAll(text, "\r", "")
			truncated, wasTruncated := tool.TruncateLine(text, tool.GrepMaxLineLength)
			if wasTruncated {
				linesTruncated = true
			}
			if current == match.line {
				outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", match.rel, current, truncated))
			} else {
				outputLines = append(outputLines, fmt.Sprintf("%s-%d- %s", match.rel, current, truncated))
			}
		}
	}

	truncation := tool.TruncateHead(strings.Join(outputLines, "\n"), tool.Limits{MaxLines: noLineLimit})
	output := truncation.Content

	var notices []string
	details := grepDetails{}
	if limitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern",
			limit, limit*2))
		details.MatchLimitReached = limit
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", tool.FormatSize(tool.DefaultMaxBytes)))
		truncationCopy := truncation
		details.Truncation = &truncationCopy
	}
	if linesTruncated {
		notices = append(notices, fmt.Sprintf(
			"Some lines truncated to %d chars. Use read tool to see full lines", tool.GrepMaxLineLength))
		details.LinesTruncated = true
	}
	if len(notices) > 0 {
		output += fmt.Sprintf("\n\n[%s]", strings.Join(notices, ". "))
	}

	result := tool.Result{Content: []wire.Content{wire.TextContent(output)}}
	if details.Truncation != nil || details.MatchLimitReached != 0 || details.LinesTruncated {
		detailsJSON, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return tool.Result{}, marshalErr
		}
		result.Details = detailsJSON
	}
	return result, nil
}

// compileGrepPattern builds the matcher. Go's regexp is RE2, the same engine
// family ripgrep uses, so a pattern that works in pi works here.
func compileGrepPattern(pattern string, ignoreCase, literal bool) (*regexp.Regexp, error) {
	if literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("Invalid pattern: %s", err)
	}
	return compiled, nil
}

// looksBinary reports whether content is binary, using the same NUL-byte
// heuristic ripgrep applies. Without it a search would return matches from
// object files and archives.
func looksBinary(content []byte) bool {
	window := content
	if len(window) > 8192 {
		window = window[:8192]
	}
	for _, b := range window {
		if b == 0 {
			return true
		}
	}
	return false
}

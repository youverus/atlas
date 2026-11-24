package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const findDescription = `Search for files by glob pattern. Returns matching file paths relative to the search directory. ` +
	`Respects .gitignore. Output is truncated to 1000 results or 50KB (whichever is hit first).`

const findParameters = `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"},
    "path": {"type": "string", "description": "Directory to search in (default: current directory)"},
    "limit": {"type": "number", "description": "Maximum number of results (default: 1000)"}
  },
  "required": ["pattern"]
}`

const findDefaultLimit = 1000

type findTool struct{ set *shared }

func (t findTool) Name() string                { return "find" }
func (t findTool) Label() string               { return "find" }
func (t findTool) Description() string         { return findDescription }
func (t findTool) Parameters() json.RawMessage { return json.RawMessage(findParameters) }

type findRequest struct {
	Pattern string       `json:"pattern"`
	Path    string       `json:"path"`
	Limit   *tool.Number `json:"limit"`
}

type findDetails struct {
	Truncation         *tool.Truncation `json:"truncation,omitempty"`
	ResultLimitReached int              `json:"resultLimitReached,omitempty"`
}

func (t findTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request findRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(request.Pattern) == "" {
		return tool.Result{}, fmt.Errorf("find: pattern is required")
	}

	searchPath := "."
	if request.Path != "" {
		searchPath = request.Path
	}
	root, err := t.set.resolveAccess(searchPath)
	if err != nil {
		return tool.Result{}, err
	}
	limit := findDefaultLimit
	if request.Limit != nil {
		limit = request.Limit.Int()
	}

	info, err := t.set.stat(root)
	if err != nil {
		if isNotExist(err) {
			return tool.Result{}, fmt.Errorf("Path not found: %s", root)
		}
		return tool.Result{}, err
	}
	if !info.IsDir() {
		return tool.Result{}, fmt.Errorf("Not a directory: %s", root)
	}

	var matches []string
	limitReached := false
	w := &walker{
		ctx:         ctx,
		set:         t.set,
		ignores:     newIgnoreStack(),
		includeDirs: true,
		visit: func(entry walkEntry) (bool, error) {
			if !matchPattern(request.Pattern, entry.Rel) {
				return true, nil
			}
			if len(matches) >= limit {
				limitReached = true
				return false, nil
			}
			matches = append(matches, toSlash(entry.Rel))
			return true, nil
		},
	}
	if err := w.walkTree(root); err != nil {
		return tool.Result{}, err
	}
	// The limit is only reached when the walk stopped on it; a tree with exactly
	// limit matches must not claim truncation.
	if len(matches) >= limit {
		limitReached = true
	}

	if len(matches) == 0 {
		return tool.Result{
			Content: []wire.Content{wire.TextContent("No files found matching pattern")},
		}, nil
	}

	truncation := tool.TruncateHead(strings.Join(matches, "\n"), tool.Limits{MaxLines: noLineLimit})
	output := truncation.Content

	var notices []string
	details := findDetails{}
	if limitReached {
		notices = append(notices, fmt.Sprintf("%d results limit reached. Use limit=%d for more, or refine pattern",
			limit, limit*2))
		details.ResultLimitReached = limit
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", tool.FormatSize(tool.DefaultMaxBytes)))
		truncationCopy := truncation
		details.Truncation = &truncationCopy
	}
	if len(notices) > 0 {
		output += fmt.Sprintf("\n\n[%s]", strings.Join(notices, ". "))
	}

	result := tool.Result{Content: []wire.Content{wire.TextContent(output)}}
	if details.Truncation != nil || details.ResultLimitReached != 0 {
		detailsJSON, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return tool.Result{}, marshalErr
		}
		result.Details = detailsJSON
	}
	return result, nil
}

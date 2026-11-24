package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const lsDescription = `List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. ` +
	`Includes dotfiles. Output is truncated to 500 entries or 50KB (whichever is hit first).`

const lsParameters = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory to list (default: current directory)"},
    "limit": {"type": "number", "description": "Maximum number of entries to return (default: 500)"}
  }
}`

const (
	lsDefaultLimit = 500
	// noLineLimit disables the line dimension of truncation. A directory listing
	// is already capped by entry count, so only bytes can overflow.
	noLineLimit = 1 << 30
)

type lsTool struct{ set *shared }

func (t lsTool) Name() string                { return "ls" }
func (t lsTool) Label() string               { return "ls" }
func (t lsTool) Description() string         { return lsDescription }
func (t lsTool) Parameters() json.RawMessage { return json.RawMessage(lsParameters) }

type lsRequest struct {
	Path  string       `json:"path"`
	Limit *tool.Number `json:"limit"`
}

type lsDetails struct {
	Truncation        *tool.Truncation `json:"truncation,omitempty"`
	EntryLimitReached int              `json:"entryLimitReached,omitempty"`
}

func (t lsTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request lsRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	searchPath := "."
	if request.Path != "" {
		searchPath = request.Path
	}
	dirPath, err := t.set.resolveAccess(searchPath)
	if err != nil {
		return tool.Result{}, err
	}
	limit := lsDefaultLimit
	if request.Limit != nil {
		limit = request.Limit.Int()
	}

	info, err := t.set.stat(dirPath)
	if err != nil {
		if isNotExist(err) {
			return tool.Result{}, fmt.Errorf("Path not found: %s", dirPath)
		}
		return tool.Result{}, err
	}
	if !info.IsDir() {
		return tool.Result{}, fmt.Errorf("Not a directory: %s", dirPath)
	}

	entries, err := t.set.readDir(dirPath)
	if err != nil {
		return tool.Result{}, fmt.Errorf("Cannot read directory: %s", pathErrorText(err))
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	// Case-insensitive alphabetical, with the raw name as a tie-break so the
	// order is stable when two entries differ only by case.
	sort.SliceStable(names, func(i, j int) bool {
		lowerI, lowerJ := strings.ToLower(names[i]), strings.ToLower(names[j])
		if lowerI == lowerJ {
			return names[i] < names[j]
		}
		return lowerI < lowerJ
	})

	results := make([]string, 0, min(limit, len(names)))
	entryLimitReached := false
	for _, name := range names {
		if len(results) >= limit {
			entryLimitReached = true
			break
		}
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		suffix := ""
		entryInfo, statErr := t.set.stat(joinAccessPath(dirPath, name))
		if statErr != nil {
			// Entries that cannot be stat'ed are dropped rather than guessed at.
			continue
		}
		if entryInfo.IsDir() {
			suffix = "/"
		}
		results = append(results, name+suffix)
	}

	if len(results) == 0 {
		return tool.Result{
			Content: []wire.Content{wire.TextContent("(empty directory)")},
		}, nil
	}

	truncation := tool.TruncateHead(strings.Join(results, "\n"), tool.Limits{MaxLines: noLineLimit})
	output := truncation.Content

	var notices []string
	details := lsDetails{}
	if entryLimitReached {
		notices = append(notices, fmt.Sprintf("%d entries limit reached. Use limit=%d for more", limit, limit*2))
		details.EntryLimitReached = limit
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
	if details.Truncation != nil || details.EntryLimitReached != 0 {
		detailsJSON, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return tool.Result{}, marshalErr
		}
		result.Details = detailsJSON
	}
	return result, nil
}

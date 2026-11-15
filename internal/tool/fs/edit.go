package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const editDescription = "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, " +
	"non-overlapping region of the original file. If two changes affect the same block or nearby lines, " +
	"merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions " +
	"just to connect distant changes."

const editParameters = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path to the file to edit (relative or absolute)"},
    "edits": {
      "type": "array",
      "description": "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
      "items": {
        "type": "object",
        "properties": {
          "oldText": {"type": "string", "description": "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},
          "newText": {"type": "string", "description": "Replacement text for this targeted edit."}
        },
        "required": ["oldText", "newText"]
      }
    }
  },
  "required": ["path", "edits"]
}`

type editTool struct{ set *shared }

func (t editTool) Name() string                { return "edit" }
func (t editTool) Label() string               { return "edit" }
func (t editTool) Description() string         { return editDescription }
func (t editTool) Parameters() json.RawMessage { return json.RawMessage(editParameters) }

// textEdit is one replacement. The JSON names are what the model sends.
type textEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editRequest struct {
	Path string `json:"path"`
	// Edits arrives in several shapes; see normalizeEdits.
	Edits json.RawMessage `json:"edits"`
	// Legacy single-edit fields, still emitted by some models.
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
}

type editDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

func (t editTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request editRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	edits, err := normalizeEdits(request)
	if err != nil {
		return tool.Result{}, err
	}
	if len(edits) == 0 {
		return tool.Result{}, errors.New("Edit tool input is invalid. edits must contain at least one replacement.")
	}

	absolute := t.set.resolve(request.Path)
	var result tool.Result
	err = t.set.queue.with(absolute, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := os.ReadFile(absolute)
		if err != nil {
			return fmt.Errorf("Could not edit file: %s. %s.", request.Path, describeAccessError(err))
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		bom, content := splitBOM(string(raw))
		originalEnding := detectLineEnding(content)
		normalized := normalizeToLF(content)

		baseContent, newContent, err := applyEdits(normalized, edits, request.Path)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		final := bom + restoreLineEndings(newContent, originalEnding)
		if err := os.WriteFile(absolute, []byte(final), 0o644); err != nil {
			return err
		}

		diff, firstChangedLine := generateDiffString(baseContent, newContent, 4)
		details := editDetails{
			Diff:  diff,
			Patch: generateUnifiedPatch(request.Path, baseContent, newContent, 4),
		}
		if firstChangedLine > 0 {
			details.FirstChangedLine = &firstChangedLine
		}
		detailsJSON, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return marshalErr
		}
		result = tool.Result{
			Content: []wire.Content{wire.TextContent(fmt.Sprintf(
				"Successfully replaced %d block(s) in %s.", len(edits), request.Path))},
			Details: detailsJSON,
		}
		return nil
	})
	if err != nil {
		return tool.Result{}, err
	}
	return result, nil
}

// normalizeEdits recovers the edits array from the shapes models emit. Opus 4.6
// and GLM-5.1 send a JSON string, others send one bare object instead of a
// one-element array, and older prompts used top-level oldText/newText.
func normalizeEdits(request editRequest) ([]textEdit, error) {
	var edits []textEdit
	raw := bytes.TrimSpace(request.Edits)

	if len(raw) > 0 && raw[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err == nil {
			raw = bytes.TrimSpace([]byte(inner))
		}
	}
	if len(raw) > 0 && raw[0] == '{' {
		var single textEdit
		if err := json.Unmarshal(raw, &single); err == nil {
			edits = append(edits, single)
		}
	} else if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &edits); err != nil {
			return nil, fmt.Errorf("edit: decode edits: %w", err)
		}
	}

	if request.OldText != nil && request.NewText != nil {
		edits = append(edits, textEdit{OldText: *request.OldText, NewText: *request.NewText})
	}
	return edits, nil
}

// applyEdits matches every edit against the original content and applies them
// together. Matching against the original is the contract the model is given:
// edits must be independent, so an edit cannot depend on an earlier one.
func applyEdits(normalizedContent string, edits []textEdit, path string) (baseContent, newContent string, err error) {
	normalized := make([]textEdit, len(edits))
	for i, edit := range edits {
		normalized[i] = textEdit{OldText: normalizeToLF(edit.OldText), NewText: normalizeToLF(edit.NewText)}
	}
	for i, edit := range normalized {
		if edit.OldText == "" {
			if len(normalized) == 1 {
				return "", "", fmt.Errorf("oldText must not be empty in %s.", path)
			}
			return "", "", fmt.Errorf("edits[%d].oldText must not be empty in %s.", i, path)
		}
	}

	usedFuzzy := false
	for _, edit := range normalized {
		if match := fuzzyFindText(normalizedContent, edit.OldText); match.found && match.usedFuzzy {
			usedFuzzy = true
			break
		}
	}
	replacementBase := normalizedContent
	if usedFuzzy {
		replacementBase = normalizeForFuzzyMatch(normalizedContent)
	}

	type matchedEdit = appliedEdit
	matched := make([]matchedEdit, 0, len(normalized))
	for i, edit := range normalized {
		match := fuzzyFindText(replacementBase, edit.OldText)
		if !match.found {
			if len(normalized) == 1 {
				return "", "", fmt.Errorf("Could not find the exact text in %s. "+
					"The old text must match exactly including all whitespace and newlines.", path)
			}
			return "", "", fmt.Errorf("Could not find edits[%d] in %s. "+
				"The oldText must match exactly including all whitespace and newlines.", i, path)
		}
		occurrences := countOccurrences(replacementBase, edit.OldText)
		if occurrences > 1 {
			if len(normalized) == 1 {
				return "", "", fmt.Errorf("Found %d occurrences of the text in %s. "+
					"The text must be unique. Please provide more context to make it unique.", occurrences, path)
			}
			return "", "", fmt.Errorf("Found %d occurrences of edits[%d] in %s. "+
				"Each oldText must be unique. Please provide more context to make it unique.",
				occurrences, i, path)
		}
		matched = append(matched, matchedEdit{
			editIndex: i,
			replacement: replacement{
				matchIndex:  match.index,
				matchLength: match.matchLength,
				newText:     edit.NewText,
			},
		})
	}

	sortAppliedEdits(matched)
	for i := 1; i < len(matched); i++ {
		previous, current := matched[i-1], matched[i]
		if previous.matchIndex+previous.matchLength > current.matchIndex {
			return "", "", fmt.Errorf("edits[%d] and edits[%d] overlap in %s. "+
				"Merge them into one edit or target disjoint regions.",
				previous.editIndex, current.editIndex, path)
		}
	}

	replacements := make([]replacement, len(matched))
	for i, entry := range matched {
		replacements[i] = entry.replacement
	}

	baseContent = normalizedContent
	if usedFuzzy {
		newContent, err = applyReplacementsPreservingUnchangedLines(normalizedContent, replacementBase, replacements)
		if err != nil {
			return "", "", err
		}
	} else {
		newContent = applyReplacements(replacementBase, replacements, 0)
	}

	if baseContent == newContent {
		if len(normalized) == 1 {
			return "", "", fmt.Errorf("No changes made to %s. The replacement produced identical content. "+
				"This might indicate an issue with special characters or the text not existing as expected.", path)
		}
		return "", "", fmt.Errorf("No changes made to %s. The replacements produced identical content.", path)
	}
	return baseContent, newContent, nil
}

// appliedEdit is one matched replacement plus the index of the edit that
// produced it, so overlap errors can name both participating edits.
type appliedEdit struct {
	editIndex int
	replacement
}

// sortAppliedEdits orders matches by position in the base content.
func sortAppliedEdits(edits []appliedEdit) {
	sort.SliceStable(edits, func(i, j int) bool {
		return edits[i].matchIndex < edits[j].matchIndex
	})
}

// fuzzyMatch is the result of looking for one oldText.
type fuzzyMatch struct {
	found       bool
	index       int
	matchLength int
	usedFuzzy   bool
}

// fuzzyFindText tries an exact match first, then a normalized one. The exact
// pass is what keeps a well-behaved edit byte-precise.
func fuzzyFindText(content, oldText string) fuzzyMatch {
	if index := strings.Index(content, oldText); index != -1 {
		return fuzzyMatch{found: true, index: index, matchLength: len(oldText)}
	}
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)
	index := strings.Index(fuzzyContent, fuzzyOldText)
	if index == -1 {
		return fuzzyMatch{}
	}
	return fuzzyMatch{found: true, index: index, matchLength: len(fuzzyOldText), usedFuzzy: true}
}

func countOccurrences(content, oldText string) int {
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)
	if fuzzyOldText == "" {
		return 0
	}
	return strings.Count(fuzzyContent, fuzzyOldText)
}

// splitBOM separates a leading byte order mark. The model cannot see a BOM, so
// it must not participate in matching, but it must survive the rewrite.
func splitBOM(content string) (bom, text string) {
	const bomChar = "\ufeff"
	if strings.HasPrefix(content, bomChar) {
		return bomChar, content[len(bomChar):]
	}
	return "", content
}

// describeAccessError renders a filesystem error with the errno name pi reports,
// so the message reads the same on every platform.
func describeAccessError(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "Error code: ENOENT"
	case errors.Is(err, fs.ErrPermission):
		return "Error code: EACCES"
	case errors.Is(err, fs.ErrExist):
		return "Error code: EEXIST"
	}
	return pathErrorText(err)
}

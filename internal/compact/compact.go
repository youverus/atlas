package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// Preparation is everything a compaction needs except the model call: which
// messages to summarize, where the kept range starts, and the summary it
// updates. Preparing is pure, so a caller can inspect or reject it first.
type Preparation struct {
	// FirstKeptEntryID is the session entry the kept range starts at.
	FirstKeptEntryID string
	// MessagesToSummarize is the range that leaves the context.
	MessagesToSummarize []wire.Message
	// TurnPrefixMessages is the head of a turn that the cut splits in two.
	TurnPrefixMessages []wire.Message
	IsSplitTurn        bool
	TokensBefore       int
	// PreviousSummary is set when the branch already carries a compaction, which
	// switches summarization to update mode.
	PreviousSummary string
	// ReadFiles and ModifiedFiles are the file operations seen in the summarized
	// range plus the ones the previous compaction recorded.
	ReadFiles     []string
	ModifiedFiles []string
}

// Result is what the caller records as a compaction entry.
type Result struct {
	Summary          string
	FirstKeptEntryID string
	TokensBefore     int
	// Usage sums the summarization calls that produced the checkpoint.
	Usage         wire.Usage
	ReadFiles     []string
	ModifiedFiles []string
}

// Details renders the file lists in the shape pi stores on a compaction entry.
// Empty lists encode as [] rather than null so the session file keeps one shape.
func (r Result) Details() (json.RawMessage, error) {
	read := r.ReadFiles
	if read == nil {
		read = []string{}
	}
	modified := r.ModifiedFiles
	if modified == nil {
		modified = []string{}
	}
	details := struct {
		ReadFiles     []string `json:"readFiles"`
		ModifiedFiles []string `json:"modifiedFiles"`
	}{ReadFiles: read, ModifiedFiles: modified}
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("compact: encode details: %w", err)
	}
	return encoded, nil
}

// Prepare decides what a compaction on this branch would do. It returns
// (nil, nil) when there is nothing to compact: the branch tip is already a
// compaction, the kept range would cover everything, or the summarized range
// holds no message that reaches the model.
func Prepare(path []session.Entry, settings Settings) (*Preparation, error) {
	settings = settings.normalized()
	if len(path) == 0 {
		return nil, nil
	}
	leaf := path[len(path)-1]
	if leaf.Type == session.TypeCompaction {
		return nil, nil
	}
	projection, err := session.BuildProjection(path, &leaf.ID)
	if err != nil {
		return nil, err
	}

	entries := projection.Entries
	boundaryStart := 0
	previousSummary := ""
	for i, entry := range entries {
		// The newest compaction is projected first and is the only one that
		// contributes a checkpoint, so the first match is the one to update.
		if entry.Source.Type == session.TypeCompaction && len(entry.Messages) > 0 {
			previousSummary = entry.Source.Summary
			boundaryStart = i + 1
			break
		}
	}
	if boundaryStart >= len(entries) {
		return nil, nil
	}

	cut := FindCutPoint(entries, boundaryStart, len(entries), settings.KeepRecentTokens)
	firstKept := entries[cut.FirstKeptEntryIndex].Source.ID
	if firstKept == "" {
		return nil, nil
	}

	historyEnd := cut.FirstKeptEntryIndex
	if cut.IsSplitTurn && cut.TurnStartIndex >= 0 {
		historyEnd = cut.TurnStartIndex
	}
	messagesToSummarize := compactionMessages(entries[boundaryStart:historyEnd])
	var turnPrefix []wire.Message
	if cut.IsSplitTurn && cut.TurnStartIndex >= 0 {
		turnPrefix = compactionMessages(entries[cut.TurnStartIndex:cut.FirstKeptEntryIndex])
	}
	if len(messagesToSummarize) == 0 && len(turnPrefix) == 0 {
		return nil, nil
	}

	files := newFileOps()
	// A previous compaction's file list is part of the summary it produced, so it
	// carries forward; a hook-written compaction has no list to carry.
	if boundaryStart > 0 {
		previous := entries[boundaryStart-1].Source
		if previous.FromHook == nil || !*previous.FromHook {
			files.addDetails(previous.Details)
		}
	}
	for _, message := range messagesToSummarize {
		files.addMessage(message)
	}
	for _, message := range turnPrefix {
		files.addMessage(message)
	}

	return &Preparation{
		FirstKeptEntryID:    firstKept,
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefix,
		IsSplitTurn:         cut.IsSplitTurn,
		TokensBefore:        EstimateProjection(projection, path).Tokens,
		PreviousSummary:     previousSummary,
		ReadFiles:           files.readOnly(),
		ModifiedFiles:       files.modified(),
	}, nil
}

// Run generates the summary for a preparation. A split turn costs two calls: one
// for the history, one for the turn head. A failure in either aborts the whole
// compaction, because a half-summarized range would lose the other half.
func (s *Summarizer) Run(ctx context.Context, preparation *Preparation, settings Settings, instructions string) (Result, error) {
	settings = settings.normalized()
	result := Result{
		FirstKeptEntryID: preparation.FirstKeptEntryID,
		TokensBefore:     preparation.TokensBefore,
		ReadFiles:        preparation.ReadFiles,
		ModifiedFiles:    preparation.ModifiedFiles,
	}

	summary := preparation.PreviousSummary
	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		// pi fills the history half with this placeholder when a checkpoint
		// already covers everything before the split turn. The text lands in the
		// session, so it is kept verbatim.
		if summary == "" {
			summary = noPriorHistory
		}
		if len(preparation.MessagesToSummarize) > 0 {
			text, usage, err := s.SummarizeHistory(ctx, preparation.MessagesToSummarize, settings, preparation.PreviousSummary, instructions)
			if err != nil {
				return Result{}, err
			}
			summary = text
			result.Usage = usage
		}
		prefix, usage, err := s.SummarizeTurnPrefix(ctx, preparation.TurnPrefixMessages, settings)
		if err != nil {
			return Result{}, err
		}
		summary += turnPrefixSeparator + prefix
		result.Usage.Add(usage)
	} else if len(preparation.MessagesToSummarize) > 0 {
		text, usage, err := s.SummarizeHistory(ctx, preparation.MessagesToSummarize, settings, preparation.PreviousSummary, instructions)
		if err != nil {
			return Result{}, err
		}
		summary = text
		result.Usage = usage
	}
	if summary == "" {
		return Result{}, fmt.Errorf("compact: nothing to summarize")
	}

	result.Summary = summary + formatFileOperations(preparation.ReadFiles, preparation.ModifiedFiles)
	return result, nil
}

// normalized fills the fields a zero Settings leaves empty. Enabled is not
// defaulted: a caller that builds Settings by hand has to opt in, which keeps an
// accidental zero value from compacting on its own.
func (s Settings) normalized() Settings {
	if s.ReserveTokens <= 0 {
		s.ReserveTokens = DefaultSettings.ReserveTokens
	}
	if s.KeepRecentTokens <= 0 {
		s.KeepRecentTokens = DefaultSettings.KeepRecentTokens
	}
	return s
}

// compactionMessages extracts what a range contributes to the context, dropping
// system messages and older compactions. The system prompt is replayed by the
// compaction entry itself, and an older compaction inside the range is already
// represented by the summary being updated.
func compactionMessages(entries []session.ProjectedEntry) []wire.Message {
	var messages []wire.Message
	for _, entry := range entries {
		if entry.Source.Type == session.TypeCompaction {
			continue
		}
		for _, message := range entry.Messages {
			if message.Role == wire.RoleSystem {
				continue
			}
			messages = append(messages, message)
		}
	}
	return messages
}

// fileOps tracks which paths the summarized range touched, so the next context
// can say what was read and what was modified without replaying the calls.
type fileOps struct {
	read    map[string]bool
	written map[string]bool
	edited  map[string]bool
}

func newFileOps() *fileOps {
	return &fileOps{read: map[string]bool{}, written: map[string]bool{}, edited: map[string]bool{}}
}

// addDetails replays a previous compaction's file lists.
func (f *fileOps) addDetails(details json.RawMessage) {
	if len(details) == 0 {
		return
	}
	var stored struct {
		ReadFiles     []string `json:"readFiles"`
		ModifiedFiles []string `json:"modifiedFiles"`
	}
	if err := json.Unmarshal(details, &stored); err != nil {
		return
	}
	for _, path := range stored.ReadFiles {
		f.read[path] = true
	}
	for _, path := range stored.ModifiedFiles {
		f.edited[path] = true
	}
}

// addMessage records the file a tool call touched. The tool names are hardcoded,
// as they are in pi: a renamed or custom tool simply contributes nothing here,
// which costs one appendix line rather than correctness.
func (f *fileOps) addMessage(message wire.Message) {
	for _, block := range message.Content.Items() {
		if block.Type != wire.ContentToolCall {
			continue
		}
		path := argumentPath(block.Arguments)
		if path == "" {
			continue
		}
		switch block.Name {
		case "read":
			f.read[path] = true
		case "write":
			f.written[path] = true
		case "edit":
			f.edited[path] = true
		}
	}
}

// readOnly reports files that were read but not modified, sorted for a stable
// summary.
func (f *fileOps) readOnly() []string {
	paths := make([]string, 0, len(f.read))
	for path := range f.read {
		if f.modifiedSet()[path] {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// modified reports files that were written or edited.
func (f *fileOps) modified() []string {
	paths := make([]string, 0, len(f.written)+len(f.edited))
	for path := range f.modifiedSet() {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (f *fileOps) modifiedSet() map[string]bool {
	out := make(map[string]bool, len(f.written)+len(f.edited))
	for path := range f.written {
		out[path] = true
	}
	for path := range f.edited {
		out[path] = true
	}
	return out
}

func argumentPath(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arguments struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return ""
	}
	return arguments.Path
}

// formatFileOperations renders the appendix pi appends to a summary.
func formatFileOperations(readFiles, modifiedFiles []string) string {
	sections := make([]string, 0, 2)
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	out := ""
	for i, section := range sections {
		if i > 0 {
			out += "\n\n"
		}
		out += section
	}
	return "\n\n" + out
}

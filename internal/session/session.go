package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pi-go/internal/wire"
)

// ErrNotFound reports an unknown entry id.
var ErrNotFound = errors.New("session: entry not found")

// Options configure a new session.
type Options struct {
	// ID is the session id. Empty generates a UUIDv7.
	ID string
	// ParentSession is the path of the session this one was forked from.
	ParentSession string
	// Clock defaults to time.Now. Tests use it for deterministic file names.
	Clock func() time.Time
}

// Manager owns one session: its header, its append-only entries, and the leaf
// pointer that decides where the next append attaches.
//
// Appends mutate the in-memory tree first and the file second. The file is
// created lazily on the first user or assistant message, so opening pi and
// closing it without chatting leaves nothing behind.
type Manager struct {
	cwd    string
	dir    string
	file   string
	id     string
	header Header

	persist bool
	flushed bool

	entries []Entry
	byID    map[string]int

	labels          map[string]string
	labelTimestamps map[string]string

	leafID *string

	clock func() time.Time
}

// Create starts a new session. An empty dir uses the default directory for cwd.
func Create(cwd, dir string, options Options) (*Manager, error) {
	resolvedCwd, err := absolutePath(cwd)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = DefaultDir(resolvedCwd, "")
	}
	resolvedDir, err := absolutePath(dir)
	if err != nil {
		return nil, err
	}
	manager := newManager(resolvedCwd, resolvedDir, true, options)
	if _, err := manager.NewSession(options); err != nil {
		return nil, err
	}
	return manager, nil
}

// Open loads an existing session file. An empty dir derives from the file's
// directory; cwdOverride replaces the header cwd.
func Open(path, dir, cwdOverride string) (*Manager, error) {
	resolvedPath, err := absolutePath(path)
	if err != nil {
		return nil, err
	}

	var header *Header
	if cwdOverride == "" {
		if _, statErr := os.Stat(resolvedPath); statErr == nil {
			header, err = ScanHeader(resolvedPath)
			if errors.Is(err, ErrHeaderTooLarge) {
				// The bounded scan is only a shortcut; a full load stays
				// authoritative for files with a very large first line.
				loaded, loadErr := LoadFile(resolvedPath)
				if loadErr != nil {
					return nil, loadErr
				}
				if !loaded.Empty() {
					header = &loaded.Header
				}
				err = nil
			}
			if err != nil {
				return nil, err
			}
		}
	}

	cwd := cwdOverride
	if cwd == "" && header != nil {
		cwd = header.Cwd
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("session: working directory: %w", err)
		}
	}
	cwd, err = absolutePath(cwd)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = filepath.Dir(resolvedPath)
	}
	resolvedDir, err := absolutePath(dir)
	if err != nil {
		return nil, err
	}

	manager := newManager(cwd, resolvedDir, true, Options{})
	if err := manager.loadFile(resolvedPath); err != nil {
		return nil, err
	}
	return manager, nil
}

// ContinueRecent opens the most recently modified session in dir, or starts a
// new one when the directory has none.
//
// The directory name is only a bucket: the cwd encoding is many-to-one, so
// /x/a-b and /x/a/b share one directory and the header cwd is the only thing
// that says which project a session belongs to. The current directory therefore
// filters the candidates in every case, the default directory included.
//
// The override passed to Open is the other half of pi's behaviour: the session
// continues in the current directory, so a project that was moved keeps
// working under -continue.
func ContinueRecent(cwd, dir string) (*Manager, error) {
	resolvedCwd, err := absolutePath(cwd)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = DefaultDir(resolvedCwd, "")
	}
	resolvedDir, err := absolutePath(dir)
	if err != nil {
		return nil, err
	}
	if recent := FindMostRecent(resolvedDir, resolvedCwd); recent != "" {
		return Open(recent, resolvedDir, resolvedCwd)
	}
	manager := newManager(resolvedCwd, resolvedDir, true, Options{})
	if _, err := manager.NewSession(Options{}); err != nil {
		return nil, err
	}
	return manager, nil
}

// InMemory returns a session that never touches the filesystem.
func InMemory(cwd string, options Options) (*Manager, error) {
	if cwd == "" {
		working, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("session: working directory: %w", err)
		}
		cwd = working
	}
	resolvedCwd, err := absolutePath(cwd)
	if err != nil {
		return nil, err
	}
	manager := newManager(resolvedCwd, "", false, options)
	if _, err := manager.NewSession(options); err != nil {
		return nil, err
	}
	return manager, nil
}

func newManager(cwd, dir string, persist bool, options Options) *Manager {
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Manager{
		cwd:             cwd,
		dir:             dir,
		persist:         persist,
		clock:           clock,
		byID:            map[string]int{},
		labels:          map[string]string{},
		labelTimestamps: map[string]string{},
	}
}

// loadFile reads path into the manager. An explicit path that does not exist
// keeps its name and becomes a new session; the file appears on the first
// persisted entry.
func (m *Manager) loadFile(path string) error {
	m.file = path
	loaded, err := LoadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if _, newErr := m.NewSession(Options{}); newErr != nil {
			return newErr
		}
		m.file = path
		return nil
	}
	if loaded.Empty() {
		// An empty file is a fresh session; a non-empty file that does not parse
		// is somebody else's data and must not be overwritten.
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
			return fmt.Errorf("%w: %s", ErrNotSession, path)
		}
		if _, newErr := m.NewSession(Options{}); newErr != nil {
			return newErr
		}
		m.file = path
		if err := SaveFile(path, &File{Header: m.header, Entries: m.entries}); err != nil {
			return err
		}
		m.flushed = true
		return nil
	}

	m.header = loaded.Header
	m.id = loaded.Header.ID
	m.entries = loaded.Entries
	m.buildIndex()
	if loaded.Migrated {
		if err := SaveFile(path, &File{Header: m.header, Entries: m.entries}); err != nil {
			return err
		}
	}
	m.flushed = true
	return nil
}

// NewSession resets the manager to an empty session, keeping cwd and dir.
func (m *Manager) NewSession(options Options) (string, error) {
	if options.ID != "" && !ValidSessionID(options.ID) {
		return "", fmt.Errorf("session: invalid session id %q", options.ID)
	}
	id := options.ID
	if id == "" {
		id = newSessionID(m.clock())
	}
	timestamp := formatTimestamp(m.clock())
	m.header = Header{
		Version:       CurrentVersion,
		ID:            id,
		Timestamp:     timestamp,
		Cwd:           m.cwd,
		ParentSession: options.ParentSession,
	}
	m.id = id
	m.entries = nil
	m.byID = map[string]int{}
	m.labels = map[string]string{}
	m.labelTimestamps = map[string]string{}
	m.leafID = nil
	m.flushed = false
	if m.persist {
		m.file = filepath.Join(m.dir, sessionFileName(timestamp, id))
	}
	return m.file, nil
}

// SetFile switches to another session file, used by resume and branch creation.
func (m *Manager) SetFile(path string) error {
	resolved, err := absolutePath(path)
	if err != nil {
		return err
	}
	return m.loadFile(resolved)
}

// Cwd returns the session working directory recorded in the header.
func (m *Manager) Cwd() string { return m.cwd }

// Dir returns the directory session files are written to.
func (m *Manager) Dir() string { return m.dir }

// ID returns the session id.
func (m *Manager) ID() string { return m.id }

// File returns the session file path, or "" for an in-memory session.
func (m *Manager) File() string { return m.file }

// Persisted reports whether entries are written to disk.
func (m *Manager) Persisted() bool { return m.persist }

// Header returns a copy of the session header.
func (m *Manager) Header() Header { return m.header }

// EntryCount returns the number of entries, excluding the header.
func (m *Manager) EntryCount() int { return len(m.entries) }

// Entries returns a copy of the entry slice. Entries are immutable once
// appended; use the Append methods to add new ones.
func (m *Manager) Entries() []Entry {
	out := make([]Entry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Entry returns the entry with the given id.
func (m *Manager) Entry(id string) (Entry, bool) {
	index, ok := m.byID[id]
	if !ok {
		return Entry{}, false
	}
	return m.entries[index], true
}

// LeafID returns the id the next append will attach to, or nil for a new root.
func (m *Manager) LeafID() *string { return m.leafID }

// LeafEntry returns the leaf entry, if the session has one.
func (m *Manager) LeafEntry() (Entry, bool) {
	if m.leafID == nil {
		return Entry{}, false
	}
	return m.Entry(*m.leafID)
}

// Children returns the direct children of an entry.
func (m *Manager) Children(parentID string) []Entry {
	var children []Entry
	for _, entry := range m.entries {
		if entry.ParentID != nil && *entry.ParentID == parentID {
			children = append(children, entry)
		}
	}
	return children
}

// Label returns the label attached to an entry, if any.
func (m *Manager) Label(id string) (string, bool) {
	label, ok := m.labels[id]
	return label, ok
}

// Name returns the latest session_info name, or "" when cleared or absent.
func (m *Manager) Name() string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Type == TypeSessionInfo {
			return strings.TrimSpace(m.entries[i].Name)
		}
	}
	return ""
}

// Branch moves the leaf to an earlier entry so the next append starts a new
// branch. History is never rewritten.
func (m *Manager) Branch(fromID string) error {
	if _, ok := m.byID[fromID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, fromID)
	}
	id := fromID
	m.leafID = &id
	return nil
}

// ResetLeaf points the leaf at nothing, so the next append becomes a new root.
func (m *Manager) ResetLeaf() { m.leafID = nil }

// BranchWithSummary branches and records a summary of the abandoned path.
func (m *Manager) BranchWithSummary(fromID *string, summary string, details json.RawMessage, fromHook *bool, usage json.RawMessage) (string, error) {
	if fromID != nil {
		if _, ok := m.byID[*fromID]; !ok {
			return "", fmt.Errorf("%w: %s", ErrNotFound, *fromID)
		}
	}
	abandoned := "root"
	if m.leafID != nil {
		abandoned = *m.leafID
	}
	m.leafID = fromID
	entry := Entry{
		Type:      TypeBranchSummary,
		FromID:    abandoned,
		Summary:   summary,
		Details:   details,
		FromHook:  fromHook,
		Usage:     usage,
		ParentID:  cloneID(fromID),
		Timestamp: formatTimestamp(m.clock()),
	}
	return m.appendEntry(entry)
}

// AppendMessage appends a message entry as a child of the current leaf.
func (m *Manager) AppendMessage(message wire.Message) (string, error) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("session: encode message: %w", err)
	}
	return m.appendEntry(Entry{
		Type:      TypeMessage,
		Message:   encoded,
		ParentID:  cloneID(m.leafID),
		Timestamp: formatTimestamp(m.clock()),
	})
}

// AppendRawMessage appends a message entry from its JSON form, for roles that
// wire.Message does not model (bashExecution, custom).
func (m *Manager) AppendRawMessage(message json.RawMessage) (string, error) {
	return m.appendEntry(Entry{
		Type:      TypeMessage,
		Message:   message,
		ParentID:  cloneID(m.leafID),
		Timestamp: formatTimestamp(m.clock()),
	})
}

// AppendThinkingLevelChange records a thinking level switch.
func (m *Manager) AppendThinkingLevelChange(level string) (string, error) {
	return m.appendEntry(Entry{
		Type:          TypeThinkingLevelChange,
		ThinkingLevel: level,
		ParentID:      cloneID(m.leafID),
		Timestamp:     formatTimestamp(m.clock()),
	})
}

// AppendModelChange records a model switch.
func (m *Manager) AppendModelChange(provider, modelID string) (string, error) {
	return m.appendEntry(Entry{
		Type:      TypeModelChange,
		Provider:  provider,
		ModelID:   modelID,
		ParentID:  cloneID(m.leafID),
		Timestamp: formatTimestamp(m.clock()),
	})
}

// AppendUsage records model-attributed usage that does not enter the context.
func (m *Manager) AppendUsage(kind, provider, model string, usage json.RawMessage, note string) (string, error) {
	return m.appendEntry(Entry{
		Type:      TypeUsage,
		Kind:      kind,
		Provider:  provider,
		Model:     model,
		Usage:     usage,
		Note:      note,
		ParentID:  cloneID(m.leafID),
		Timestamp: formatTimestamp(m.clock()),
	})
}

// CompactionOptions carry the parts of a compaction entry that come from the
// summarisation call rather than from the session.
type CompactionOptions struct {
	Details  json.RawMessage
	Usage    json.RawMessage
	Provider string
	Model    string
	FromHook *bool
}

// AppendCompaction records a compaction boundary. The current system prompt is
// captured too, so a resume does not depend on how the prompt is rebuilt later.
func (m *Manager) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, options CompactionOptions) (string, error) {
	id := newEntryID(m.byID)
	if firstKeptEntryID == "" {
		firstKeptEntryID = id
	}
	entry := Entry{
		Type:             TypeCompaction,
		ID:               id,
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     json.Number(fmt.Sprintf("%d", tokensBefore)),
		Details:          options.Details,
		Usage:            options.Usage,
		Provider:         options.Provider,
		Model:            options.Model,
		FromHook:         options.FromHook,
		ParentID:         cloneID(m.leafID),
		Timestamp:        formatTimestamp(m.clock()),
	}
	context, err := BuildContext(m.entries, m.leafID)
	if err != nil {
		return "", err
	}
	if system, ok := wire.CurrentSystemMessage(context.Messages); ok {
		timestamp := m.clock().UnixMilli()
		system.Timestamp = timestamp
		encoded, err := json.Marshal(system)
		if err != nil {
			return "", fmt.Errorf("session: encode system message: %w", err)
		}
		entry.SystemMessage = encoded
	}
	return m.appendEntry(entry)
}

// AppendCustomEntry stores extension state that does not enter the context.
func (m *Manager) AppendCustomEntry(customType string, data json.RawMessage) (string, error) {
	return m.appendEntry(Entry{
		Type:       TypeCustom,
		CustomType: customType,
		Data:       data,
		ParentID:   cloneID(m.leafID),
		Timestamp:  formatTimestamp(m.clock()),
	})
}

// AppendSessionInfo records the display name. Empty names clear it.
func (m *Manager) AppendSessionInfo(name string) (string, error) {
	return m.appendEntry(Entry{
		Type:      TypeSessionInfo,
		Name:      sanitizeName(name),
		ParentID:  cloneID(m.leafID),
		Timestamp: formatTimestamp(m.clock()),
	})
}

// AppendCustomMessage appends an extension message. display controls rendering,
// not context: custom messages always reach the model.
func (m *Manager) AppendCustomMessage(customType string, content json.RawMessage, display bool, details json.RawMessage) (string, error) {
	return m.appendEntry(Entry{
		Type:       TypeCustomMessage,
		CustomType: customType,
		Content:    content,
		Display:    &display,
		Details:    details,
		ParentID:   cloneID(m.leafID),
		Timestamp:  formatTimestamp(m.clock()),
	})
}

// AppendContextEdit rewrites one earlier entry's contribution to the context.
// A nil replacement omits the entry entirely.
func (m *Manager) AppendContextEdit(targetID string, replacement json.RawMessage) (string, error) {
	target, ok := m.Entry(targetID)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, targetID)
	}
	if !m.onActiveBranch(targetID) {
		return "", fmt.Errorf("session: entry %s is not on the active branch", targetID)
	}

	var role string
	switch target.Type {
	case TypeCustomMessage:
		role = "custom"
	case TypeMessage:
		role = messageRole(target)
		if role != string(wire.RoleUser) && role != string(wire.RoleAssistant) && role != string(wire.RoleToolResult) {
			return "", fmt.Errorf("session: entry %s does not contribute editable model content", targetID)
		}
	default:
		return "", fmt.Errorf("session: entry %s does not contribute editable model content", targetID)
	}

	normalized := replacement
	if !isNull(replacement) && (role == string(wire.RoleAssistant) || role == string(wire.RoleToolResult)) {
		var edit struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(replacement, &edit); err == nil && !isNull(edit.Content) && isJSONString(edit.Content) {
			var text string
			if err := json.Unmarshal(edit.Content, &text); err == nil {
				// Assistant and tool result content is always a block array.
				blocks, marshalErr := json.Marshal([]wire.Content{wire.TextContent(text)})
				if marshalErr != nil {
					return "", fmt.Errorf("session: encode context edit: %w", marshalErr)
				}
				normalized = json.RawMessage(`{"content":` + string(blocks) + `}`)
			}
		}
	}
	return m.appendEntry(Entry{
		Type:        TypeContextEdit,
		TargetID:    targetID,
		Replacement: normalized,
		ParentID:    cloneID(m.leafID),
		Timestamp:   formatTimestamp(m.clock()),
	})
}

func isJSONString(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, `"`)
}

// AppendLabelChange labels or unlabels an entry. Labels live in the tree as
// entries, so a label survives a resume and can be replayed.
func (m *Manager) AppendLabelChange(targetID string, label *string) (string, error) {
	if _, ok := m.Entry(targetID); !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, targetID)
	}
	id, err := m.appendEntry(Entry{
		Type:      TypeLabel,
		TargetID:  targetID,
		Label:     label,
		ParentID:  cloneID(m.leafID),
		Timestamp: formatTimestamp(m.clock()),
	})
	if err != nil {
		return "", err
	}
	if label != nil && *label != "" {
		m.labels[targetID] = *label
		m.labelTimestamps[targetID] = m.entries[m.byID[id]].Timestamp
	} else {
		delete(m.labels, targetID)
		delete(m.labelTimestamps, targetID)
	}
	return id, nil
}

// Path returns the entries from the root to fromID, or to the current leaf when
// fromID is empty.
func (m *Manager) Path(fromID string) []Entry {
	var leaf *string
	if fromID != "" {
		leaf = &fromID
	} else {
		leaf = m.leafID
	}
	return BuildPath(m.entries, leaf)
}

// BuildContextEntries returns the compaction-aware entry list for the leaf.
func (m *Manager) BuildContextEntries() []Entry {
	return BuildContextEntries(m.entries, m.leafID)
}

// BuildProjection returns the messages and settings for the current leaf.
func (m *Manager) BuildProjection() (*Projection, error) {
	return BuildProjection(m.entries, m.leafID)
}

// BuildContext returns what a provider request needs for the current leaf.
func (m *Manager) BuildContext() (*Context, error) {
	return BuildContext(m.entries, m.leafID)
}

// TreeNode is one node of the session tree, with its resolved label.
type TreeNode struct {
	Entry          Entry
	Children       []*TreeNode
	Label          *string
	LabelTimestamp string
}

// Tree returns the session as a tree. Broken parent links become roots instead of
// dropping entries, so a corrupted file stays inspectable.
func (m *Manager) Tree() []*TreeNode {
	nodes := make(map[string]*TreeNode, len(m.entries))
	for _, entry := range m.entries {
		node := &TreeNode{Entry: entry}
		if label, ok := m.labels[entry.ID]; ok {
			value := label
			node.Label = &value
			node.LabelTimestamp = m.labelTimestamps[entry.ID]
		}
		nodes[entry.ID] = node
	}
	var roots []*TreeNode
	for _, entry := range m.entries {
		node := nodes[entry.ID]
		switch {
		case entry.ParentID == nil || *entry.ParentID == entry.ID:
			roots = append(roots, node)
		default:
			parent, ok := nodes[*entry.ParentID]
			if !ok {
				roots = append(roots, node)
				continue
			}
			parent.Children = append(parent.Children, node)
		}
	}
	sortTreeNodes(roots)
	return roots
}

func sortTreeNodes(nodes []*TreeNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		return millis(nodes[i].Entry.Timestamp) < millis(nodes[j].Entry.Timestamp)
	})
	for _, node := range nodes {
		sortTreeNodes(node.Children)
	}
}

// appendEntry attaches an entry to the current leaf and persists it.
func (m *Manager) appendEntry(entry Entry) (string, error) {
	if entry.ID == "" {
		entry.ID = newEntryID(m.byID)
	}
	if entry.Timestamp == "" {
		entry.Timestamp = formatTimestamp(m.clock())
	}
	if err := m.persistEntry(entry); err != nil {
		return "", err
	}
	m.entries = append(m.entries, entry)
	index := len(m.entries) - 1
	m.byID[entry.ID] = index
	id := entry.ID
	m.leafID = &id
	return entry.ID, nil
}

// persistEntry writes according to the lazy-creation rule.
func (m *Manager) persistEntry(entry Entry) error {
	if !m.persist || m.file == "" {
		return nil
	}
	if !m.flushed {
		if !m.hasConversation(entry) {
			return nil
		}
		file := &File{Header: m.header, Entries: append(m.Entries(), entry)}
		if err := CreateFile(m.file, file); err != nil {
			return err
		}
		m.flushed = true
		return nil
	}
	return AppendEntry(m.file, entry)
}

// hasConversation reports whether the session, with pending included, holds a
// user or assistant message, which is what makes a session worth persisting.
func (m *Manager) hasConversation(pending Entry) bool {
	if isConversationEntry(pending) {
		return true
	}
	return m.hasConversationEntries()
}

func (m *Manager) hasConversationEntries() bool {
	for _, entry := range m.entries {
		if isConversationEntry(entry) {
			return true
		}
	}
	return false
}

func isConversationEntry(entry Entry) bool {
	if entry.Type != TypeMessage {
		return false
	}
	role := messageRole(entry)
	return role == string(wire.RoleUser) || role == string(wire.RoleAssistant)
}

// buildIndex rebuilds the id index, leaf pointer, and label maps from entries.
func (m *Manager) buildIndex() {
	m.byID = make(map[string]int, len(m.entries))
	m.labels = map[string]string{}
	m.labelTimestamps = map[string]string{}
	m.leafID = nil
	for i, entry := range m.entries {
		m.byID[entry.ID] = i
		id := entry.ID
		m.leafID = &id
		if entry.Type != TypeLabel {
			continue
		}
		if entry.Label != nil && *entry.Label != "" {
			m.labels[entry.TargetID] = *entry.Label
			m.labelTimestamps[entry.TargetID] = entry.Timestamp
		} else {
			delete(m.labels, entry.TargetID)
			delete(m.labelTimestamps, entry.TargetID)
		}
	}
}

// onActiveBranch reports whether id is on the root-to-leaf path.
func (m *Manager) onActiveBranch(id string) bool {
	for _, entry := range BuildPath(m.entries, m.leafID) {
		if entry.ID == id {
			return true
		}
	}
	return false
}

// cloneID copies an id pointer so callers cannot alias manager state.
func cloneID(id *string) *string {
	if id == nil {
		return nil
	}
	value := *id
	return &value
}

// sessionFileName renders `<timestamp>_<id>.jsonl` with the characters that are
// illegal or awkward in file names replaced, matching pi.
func sessionFileName(timestamp, id string) string {
	replacer := strings.NewReplacer(":", "-", ".", "-")
	return replacer.Replace(timestamp) + "_" + id + ".jsonl"
}

// absolutePath cleans a path into an absolute one.
func absolutePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("session: empty path")
	}
	expanded := path
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("session: home directory: %w", err)
		}
		expanded = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("session: resolve %s: %w", path, err)
	}
	return absolute, nil
}

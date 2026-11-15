package session

import (
	"fmt"
	"path/filepath"
	"sort"
)

// CreateBranchedSession writes the path from the root to leafID as a new
// session in the same directory, so a branch can be resumed on its own. An empty
// leafID branches from the current leaf; an id that is not in the session
// branches from the last entry, which is what pi's path builder does. It returns
// the new file path, or "" for an in-memory session.
//
// Labels are dropped from the chain. They are real tree entries in pi, so a
// later entry can be a child of a label; removing one means re-chaining the
// retained entries and re-pointing a compaction whose first kept entry was a
// label at the entry that followed it. The labels themselves survive as a tail
// chain appended after the last retained entry.
func (m *Manager) CreateBranchedSession(leafID string) (string, error) {
	path := m.Path(leafID)
	if len(path) == 0 {
		return "", fmt.Errorf("%w: %q", ErrNotFound, leafID)
	}
	previousFile := m.file

	retained := make([]Entry, 0, len(path))
	replacementByLabelID := make(map[string]string, len(path))
	var pendingLabels []string
	var pathParentID *string
	for _, entry := range path {
		if entry.Type == TypeLabel {
			pendingLabels = append(pendingLabels, entry.ID)
			continue
		}
		for _, labelID := range pendingLabels {
			replacementByLabelID[labelID] = entry.ID
		}
		pendingLabels = nil
		branched := entry
		if !sameID(entry.ParentID, pathParentID) {
			branched.SetParentID(cloneID(pathParentID))
		}
		// A compaction that kept its own entry keeps doing so; one that kept a
		// dropped label follows the label's replacement.
		if entry.Type == TypeCompaction && entry.FirstKeptEntryID != entry.ID {
			if replacement, ok := replacementByLabelID[entry.FirstKeptEntryID]; ok {
				branched.SetFirstKeptEntryID(replacement)
			}
		}
		retained = append(retained, branched)
		id := entry.ID
		pathParentID = &id
	}

	taken := make(map[string]int, len(retained)+len(m.labels))
	for _, entry := range retained {
		taken[entry.ID] = 0
	}

	type pendingLabel struct {
		targetID  string
		label     string
		timestamp string
	}
	var labels []pendingLabel
	for targetID, label := range m.labels {
		if _, ok := taken[targetID]; !ok {
			continue
		}
		labels = append(labels, pendingLabel{targetID, label, m.labelTimestamps[targetID]})
	}
	// pi walks its label map in insertion order. Sorting by label timestamp and
	// then target keeps the output deterministic across runs.
	sort.Slice(labels, func(i, j int) bool {
		if labels[i].timestamp != labels[j].timestamp {
			return labels[i].timestamp < labels[j].timestamp
		}
		return labels[i].targetID < labels[j].targetID
	})

	entries := retained
	if len(retained) > 0 {
		parentID := retained[len(retained)-1].ID
		for _, label := range labels {
			id := newEntryID(taken)
			taken[id] = 0
			value := label.label
			entries = append(entries, Entry{
				Type:      TypeLabel,
				ID:        id,
				ParentID:  cloneID(&parentID),
				Timestamp: label.timestamp,
				TargetID:  label.targetID,
				Label:     &value,
			})
			parentID = id
		}
	}

	timestamp := formatTimestamp(m.clock())
	id := newSessionID(m.clock())
	header := Header{
		Version:   CurrentVersion,
		ID:        id,
		Timestamp: timestamp,
		Cwd:       m.cwd,
	}
	if m.persist {
		header.ParentSession = previousFile
		m.file = filepath.Join(m.dir, sessionFileName(timestamp, id))
	}

	m.header = header
	m.id = id
	m.entries = entries
	m.buildIndex()

	if !m.persist {
		m.flushed = false
		return "", nil
	}
	// Same rule as the append path: a branch with no conversation stays in
	// memory until the first user or assistant message.
	if m.hasConversationEntries() {
		if err := SaveFile(m.file, &File{Header: header, Entries: entries}); err != nil {
			return "", err
		}
		m.flushed = true
	} else {
		m.flushed = false
	}
	return m.file, nil
}

// ForkFrom copies a session into another project: the new session keeps every
// entry from the source and records the source file as its parent.
func ForkFrom(sourcePath, targetCwd, dir string, options Options) (*Manager, error) {
	resolvedSource, err := absolutePath(sourcePath)
	if err != nil {
		return nil, err
	}
	source, err := LoadFile(resolvedSource)
	if err != nil {
		return nil, err
	}
	if source.Empty() {
		return nil, fmt.Errorf("%w: source session is empty or invalid: %s", ErrNotSession, resolvedSource)
	}
	resolvedTarget, err := absolutePath(targetCwd)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = DefaultDir(resolvedTarget, "")
	}
	resolvedDir, err := absolutePath(dir)
	if err != nil {
		return nil, err
	}

	manager := newManager(resolvedTarget, resolvedDir, true, options)
	if _, err := manager.NewSession(Options{
		ID:            options.ID,
		ParentSession: resolvedSource,
		Clock:         options.Clock,
	}); err != nil {
		return nil, err
	}
	if err := CreateFile(manager.file, &File{Header: manager.header, Entries: source.Entries}); err != nil {
		return nil, err
	}
	manager.entries = source.Entries
	manager.buildIndex()
	manager.flushed = true
	return manager, nil
}

// sameID reports whether two optional entry ids point at the same value.
func sameID(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

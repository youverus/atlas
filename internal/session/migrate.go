package session

import (
	"encoding/json"
	"strconv"
)

// migrate upgrades entries in place to CurrentVersion and reports whether
// anything changed. Only the shapes pi itself migrates are handled; a file at
// the current version is left untouched so its bytes stay identical.
func migrate(header *Header, entries []Entry) bool {
	version := header.Version
	if version == 0 {
		// v1 sessions predate the version field.
		version = 1
	}
	if version >= CurrentVersion {
		return false
	}
	if version < 2 {
		migrateV1ToV2(entries)
	}
	if version < 3 {
		migrateV2ToV3(entries)
	}
	header.Version = CurrentVersion
	return true
}

// migrateV1ToV2 adds the id/parentId tree and converts the compaction index into
// an entry id.
func migrateV1ToV2(entries []Entry) {
	taken := make(map[string]int, len(entries))
	var previous *string
	for i := range entries {
		entry := &entries[i]
		id := newEntryID(taken)
		taken[id] = i
		entry.ID = id
		entry.ParentID = previous
		previous = &entries[i].ID
		entry.invalidate()
	}
	// The index refers to the file's line order, header included, so entry i in
	// this slice is index i+1.
	for i := range entries {
		entry := &entries[i]
		if entry.Type != TypeCompaction {
			continue
		}
		raw, ok := entry.extra["firstKeptEntryIndex"]
		if !ok {
			continue
		}
		index, err := strconv.Atoi(string(raw))
		if err == nil && index >= 1 && index <= len(entries) {
			entry.FirstKeptEntryID = entries[index-1].ID
		}
		delete(entry.extra, "firstKeptEntryIndex")
		entry.invalidate()
	}
}

// migrateV2ToV3 renames the hookMessage role to custom.
func migrateV2ToV3(entries []Entry) {
	for i := range entries {
		entry := &entries[i]
		if entry.Type != TypeMessage || len(entry.Message) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry.Message, &fields); err != nil {
			continue
		}
		var role string
		if err := json.Unmarshal(fields["role"], &role); err != nil || role != "hookMessage" {
			continue
		}
		fields["role"] = json.RawMessage(`"custom"`)
		encoded, err := json.Marshal(fields)
		if err != nil {
			continue
		}
		entry.Message = encoded
		entry.invalidate()
	}
}

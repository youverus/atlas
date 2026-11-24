package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ArchiveSuffix marks a session file as archived. The mark is a sidecar file
// beside the session, not a field inside it: the v3 JSONL is a compatibility
// contract with pi, and a flag pi does not write has no place in it. The
// presence of the file is the whole state, so there is nothing inside it that
// can disagree with the session it marks.
const ArchiveSuffix = ".archived"

// ErrNoSessionFile reports an archive request for a session that has no file.
// Lazy creation means a conversation nobody talked to has nothing on disk, and
// hiding nothing is not archiving.
var ErrNoSessionFile = errors.New("session: no session file to archive")

// ArchiveMarker is the path of the sidecar that marks path as archived.
func ArchiveMarker(path string) string { return path + ArchiveSuffix }

// Archived reports whether the session at path was archived.
func Archived(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(ArchiveMarker(path))
	return err == nil
}

// Archive marks the session at path as archived. Archiving twice is not an
// error: the state is the marker, and it is already there.
func Archive(path string) error {
	if path == "" {
		return ErrNoSessionFile
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNoSessionFile
		}
		return fmt.Errorf("session: archive %s: %w", path, err)
	}
	if err := os.WriteFile(ArchiveMarker(path), nil, 0o644); err != nil {
		return fmt.Errorf("session: archive %s: %w", path, err)
	}
	return nil
}

// Unarchive lifts the mark. A session that was never archived is left alone, so
// restoring twice says the same thing as restoring once.
func Unarchive(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(ArchiveMarker(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("session: unarchive %s: %w", path, err)
	}
	return nil
}

// SubagentDir returns the path of a session's background task records.
func SubagentDir(path string) string { return path + ".subagents" }

// ArtifactDir returns the path of a session's saved artifact directory.
func ArtifactDir(path string) string { return path + ".artifacts" }

// DeleteFile removes a session, its task records, its artifact directory, and its archive mark. The session goes first: a marker
// whose session is gone is inert (nothing lists it), while a session whose
// marker is gone is a live conversation the user asked to destroy.
//
// Whether the session may be deleted at all is the caller's rule, not this
// function's: "only an archived session can be deleted" is a policy about what
// the user is looking at.
func DeleteFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("session: delete %s: %w", path, err)
	}
	if err := os.RemoveAll(SubagentDir(path)); err != nil {
		return fmt.Errorf("session: delete background tasks for %s: %w", path, err)
	}
	if err := os.RemoveAll(ArtifactDir(path)); err != nil {
		return fmt.Errorf("session: delete artifacts for %s: %w", path, err)
	}
	return Unarchive(path)
}

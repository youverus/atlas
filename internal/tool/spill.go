package tool

import (
	"os"
	"path/filepath"
)

// Spill is the destination for output the model cannot see in full: the whole
// text goes into a file and the notice names the path, so shortening an answer
// never destroys the tail. Every output file is created here, which is the
// point — where they live can change in one place instead of four.
//
// The directory is a function rather than a path because the tools outlive the
// conversation they were built for. The set is rebuilt when a session's
// directory changes, not when a different session opens in the same project, so
// a /resume into another conversation has to move the spills by asking again
// rather than by rebuilding anything.
//
// A nil *Spill is usable and means the OS temp directory: that is where every
// output file went before 0.12.8, and a caller with nowhere better to put them
// still gets a working notice.
type Spill struct {
	dir func() string
}

// NewSpill returns a Spill that asks dir for its directory each time it writes.
// A nil dir means the OS temp directory.
func NewSpill(dir func() string) *Spill { return &Spill{dir: dir} }

// Dir reports where the next output file would go. It is never empty: with no
// directory configured it is the OS temp directory.
func (s *Spill) Dir() string {
	if s == nil || s.dir == nil {
		return os.TempDir()
	}
	if dir := s.dir(); dir != "" {
		return dir
	}
	return os.TempDir()
}

// Create opens a new output file for output that is still arriving; the caller
// writes and closes it. It is what the shell accumulator needs, because a
// command that prints a gigabyte has to cost a gigabyte of disk rather than of
// memory.
func (s *Spill) Create(prefix, suffix string) (*os.File, error) {
	dir := s.Dir()
	pattern := prefix + "-*" + suffix
	if err := os.MkdirAll(dir, 0o700); err != nil {
		// A spill is a courtesy: the caller already has the truncated text, and
		// failing the tool call over where the file went would lose the answer
		// along with the tail.
		return os.CreateTemp("", pattern)
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return os.CreateTemp("", pattern)
	}
	return file, nil
}

// Write stores data in a new output file and returns its path. Output can carry
// private data, so the file is 0600 and created with O_EXCL (os.CreateTemp does
// both): a spill must never follow a link someone else placed at the path.
func (s *Spill) Write(prefix, suffix string, data []byte) (string, error) {
	file, err := s.Create(prefix, suffix)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// SpillNotice is the clause every truncation notice ends with, so the four
// tools that spill say "where it went" the same way. What the model should do
// instead stays with each tool: narrowing a grep and re-reading a page are not
// the same instruction.
func SpillNotice(path string) string {
	if path == "" {
		return ""
	}
	return "the full output is in " + filepath.Clean(path)
}

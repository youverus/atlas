package task

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// FileName is the record file inside the session directory.
//
// It is one file per project rather than one per session because a task list
// outlives a single conversation: /new starts another session in the same
// directory, and the commands it should still be able to see are the ones the
// previous session left. Filtering by owner is what keeps that from leaking —
// the file is shared, the answer is not.
const FileName = "tasks.jsonl"

// Store is an append-only record file.
//
// Every change is a new line, and the last line for an id wins. That is the
// same rule the session log follows, and it buys the same thing: a crash in the
// middle of a write can lose the last line but can never leave a record that
// contradicts itself. The file is rewritten by nobody, so two processes
// appending to it do not clobber each other's rows.
type Store struct {
	dir func() string

	mu sync.Mutex
	// taken is the set of ids in the file, kept so a fresh id can be checked
	// against them instead of walking every record on each create.
	taken map[string]bool
}

// NewStore returns a store that asks dir for its directory each time.
//
// The directory is a function because a session can be resumed from another
// project: the tools outlive the conversation, and a captured path would write
// this project's tasks into the last project's file.
func NewStore(dir func() string) *Store {
	return &Store{dir: dir}
}

// Path is where the records are written.
func (s *Store) Path() string {
	return filepath.Join(s.dir(), FileName)
}

// Load reads every record and returns one task per id, newest record winning.
//
// A malformed line is skipped rather than fatal: the only way to get one is a
// write that was cut off by a crash, and losing the report of one command must
// not make the whole list unreadable.
func (s *Store) Load() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.Path()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		s.taken = map[string]bool{}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	byID := map[string]Task{}
	order := make([]string, 0, 8)
	scanner := bufio.NewScanner(file)
	// A record carrying a long command can pass the default 64KB token limit,
	// and the limit is per line: a 1MB buffer costs nothing until a line needs
	// it.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Task
		if err := json.Unmarshal([]byte(line), &record); err != nil || record.ID == "" {
			continue
		}
		if _, seen := byID[record.ID]; !seen {
			order = append(order, record.ID)
		}
		byID[record.ID] = record
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		return nil, err
	}

	tasks := make([]Task, 0, len(order))
	s.taken = make(map[string]bool, len(order))
	for _, id := range order {
		tasks = append(tasks, byID[id])
		s.taken[id] = true
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreatedAtMs != tasks[j].CreatedAtMs {
			return tasks[i].CreatedAtMs < tasks[j].CreatedAtMs
		}
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, nil
}

// Append writes one record.
func (s *Store) Append(record Task) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(s.Path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// One write per record: an append of a line under the platform's atomic
	// write size cannot interleave with another process's append.
	_, writeErr := file.Write(append(payload, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if s.taken == nil {
		s.taken = map[string]bool{}
	}
	s.taken[record.ID] = true
	return nil
}

// nextID returns an id no record in the file uses yet.
func (s *Store) nextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return newID(s.taken)
}

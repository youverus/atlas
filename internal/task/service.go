package task

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"pi-go/internal/tool"
)

// ErrNotFound reports an unknown task id.
var ErrNotFound = errors.New("task: not found")

// ErrFinished reports a stop aimed at a task that already ended.
var ErrFinished = errors.New("task: already finished")

// ErrUnsupported reports an operation this platform cannot do.
var ErrUnsupported = errors.New("task: unsupported on this platform")

// Options configure a Service.
type Options struct {
	// Dir answers the session directory holding the record file.
	Dir func() string
	// OwnerSession answers the conversation a new task belongs to. It is asked
	// per task rather than captured, because /new and /resume change the answer
	// while the tools stay built.
	OwnerSession func() string
	// PID is the process that runs the tasks. Zero means this process.
	PID int
	// Now is the clock. Tests replace it.
	Now func() time.Time
	// Create opens the file a run's output goes to. The caller decides where
	// output files live (the tool spill, in atlas); a nil Create keeps only the
	// record and drops the output, which is what a host without a writable
	// directory can still do.
	Create func(prefix string) (*os.File, error)
}

// Service is the registry of background commands for one process.
//
// It holds no list of its own: every read goes back to the file. That is a
// deliberate trade. The file is small (a task is a few hundred bytes and a
// project has a few dozen), a read is once per tool call rather than once per
// frame, and the alternative is a cache that has to be invalidated when another
// process appends a row — which is exactly the bug that would be hardest to
// see.
type Service struct {
	options Options
	store   *Store

	mu     sync.Mutex
	closed bool
}

// New returns a service over dir.
func New(options Options) *Service {
	if options.PID == 0 {
		options.PID = os.Getpid()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.OwnerSession == nil {
		options.OwnerSession = func() string { return "" }
	}
	return &Service{options: options, store: NewStore(options.Dir)}
}

// Register creates the row for a command that is about to start and returns the
// file its output goes to.
//
// The row is written before the process starts, so a crash between the two
// leaves a queued task rather than a running command nobody recorded.
func (s *Service) Register(spec tool.Background) (string, *os.File, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", nil, errors.New("task: the service is closed")
	}
	s.mu.Unlock()

	if _, err := s.store.Load(); err != nil {
		return "", nil, err
	}
	id := s.store.nextID()
	now := s.options.Now()
	record := Task{
		ID:             id,
		Kind:           KindBash,
		Status:         StatusQueued,
		OwnerSessionID: s.options.OwnerSession(),
		OwnerPID:       s.options.PID,
		Command:        spec.Command,
		Description:    spec.Description,
		Cwd:            spec.Cwd,
		ToolCallID:     spec.ToolCallID,
		CreatedAtMs:    now.UnixMilli(),
	}
	if s.options.Create != nil {
		file, err := s.options.Create(id)
		if err != nil {
			// A task with no output file is still worth recording: the command
			// runs, and task_output says there is nothing to read. Losing the
			// record over where the output went would hide the command.
			file = nil
		} else {
			record.OutputPath = file.Name()
		}
		if err := s.store.Append(record); err != nil {
			if file != nil {
				_ = file.Close()
			}
			return "", nil, err
		}
		return id, file, nil
	}
	if err := s.store.Append(record); err != nil {
		return "", nil, err
	}
	return id, nil, nil
}

// Started records that the process exists, and which group it leads.
func (s *Service) Started(id string, pgid int) {
	current, err := s.get(id)
	if err != nil {
		return
	}
	next := current
	next.Status = StatusRunning
	next.PGID = pgid
	next.StartedAtMs = s.options.Now().UnixMilli()
	s.appendIfLegal(current, next)
}

// Settled records how a run ended.
//
// A terminal status is final, so a stop that landed first keeps the row it
// wrote: the process did end, but the row describes why, and "canceled" is the
// true answer to why.
func (s *Service) Settled(id string, exit tool.BackgroundExit) {
	current, err := s.get(id)
	if err != nil || current.Status.Terminal() {
		return
	}
	next := current
	switch {
	case current.Status == StatusStopping:
		next.Status = StatusCanceled
	case exit.Err != nil:
		next.Status = StatusFailed
		next.Error = exit.Err.Error()
	case exit.Aborted:
		next.Status = StatusCanceled
	case exit.TimedOut:
		next.Status = StatusFailed
		next.Error = "timed out"
	case exit.Code == 0:
		next.Status = StatusSucceeded
	default:
		next.Status = StatusFailed
		code := exit.Code
		next.ExitCode = &code
	}
	if next.Status != StatusCanceled && exit.Err == nil {
		code := exit.Code
		next.ExitCode = &code
	}
	next.EndedAtMs = exit.EndedAt.UnixMilli()
	if next.EndedAtMs == 0 {
		next.EndedAtMs = s.options.Now().UnixMilli()
	}
	if exit.StartedAt.UnixMilli() > 0 && next.StartedAtMs == 0 {
		next.StartedAtMs = exit.StartedAt.UnixMilli()
	}
	s.appendIfLegal(current, next)
}

// Filter narrows a listing.
type Filter struct {
	// ID asks for one task.
	ID string
	// Statuses keeps only these. Empty keeps everything.
	Statuses []Status
	// AllSessions includes tasks owned by other conversations. The default is
	// the current one, which is what a listing should answer: "what did I
	// start", not "what did anyone ever start here".
	AllSessions bool
	// OwnerSession is the conversation to filter by. Empty means ask the
	// service.
	OwnerSession string
}

// Query returns the matching tasks, oldest first.
//
// A task this process owns is live by definition; one owned by another process
// is live only if that process still exists. The second half is decided here,
// at read time, and never written back: a record that says running is the truth
// about what was written, and a listing that says lost is the truth about now.
// Writing the second one into the file would make the row disagree with a
// process that was merely suspended.
func (s *Service) Query(filter Filter) ([]Task, error) {
	all, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	owner := filter.OwnerSession
	if owner == "" {
		owner = s.options.OwnerSession()
	}
	out := make([]Task, 0, len(all))
	for _, record := range all {
		if filter.ID != "" && record.ID != filter.ID {
			continue
		}
		if !filter.AllSessions && owner != "" && record.OwnerSessionID != owner {
			continue
		}
		if len(filter.Statuses) > 0 && !containsStatus(filter.Statuses, record.Status) {
			continue
		}
		out = append(out, s.resolve(record))
	}
	return out, nil
}

// Get returns one task, resolved the same way a listing does.
func (s *Service) Get(id string) (Task, error) {
	all, err := s.store.Load()
	if err != nil {
		return Task{}, err
	}
	for _, record := range all {
		if record.ID == id {
			return s.resolve(record), nil
		}
	}
	return Task{}, ErrNotFound
}

// resolve turns a stale "running" row into "lost" when the process that owned
// it is gone, without touching the file.
func (s *Service) resolve(record Task) Task {
	if !record.Status.Active() || record.OwnerPID == 0 || record.OwnerPID == s.options.PID {
		return record
	}
	if processAlive(record.OwnerPID) {
		return record
	}
	record.Status = StatusLost
	record.Error = "the process that ran this task is gone"
	return record
}

// ReadResult is one slice of a task's output.
type ReadResult struct {
	Content string
	// NextOffset is where a following read continues.
	NextOffset int64
	// TotalBytes is the size of the whole file.
	TotalBytes int64
	// Done is set when nothing more can arrive: the task is terminal and the
	// reader reached the end.
	Done bool
}

// Read returns output from offset, at most limit bytes.
//
// The file is read rather than a buffer, because the process may still be
// writing: a reader that keeps its own copy would answer from a snapshot and
// hide everything that arrived since.
func (s *Service) Read(id string, offset int64, limit int) (ReadResult, error) {
	record, err := s.Get(id)
	if err != nil {
		return ReadResult{}, err
	}
	if record.OutputPath == "" {
		return ReadResult{Done: !record.Status.Active()}, nil
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	file, err := os.Open(record.OutputPath)
	if errors.Is(err, os.ErrNotExist) {
		return ReadResult{Done: !record.Status.Active()}, nil
	}
	if err != nil {
		return ReadResult{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ReadResult{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ReadResult{}, err
	}
	buffer := make([]byte, limit)
	read, readErr := file.Read(buffer)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return ReadResult{}, readErr
	}
	next := offset + int64(read)
	return ReadResult{
		Content:    string(buffer[:read]),
		NextOffset: next,
		TotalBytes: info.Size(),
		Done:       !record.Status.Active() && next >= info.Size(),
	}, nil
}

// Stop signals a task's process group and records that it was stopped.
//
// The signal goes first and the record is re-read afterwards, because a
// process that exits between the two would otherwise be reported as stopped
// when it finished on its own.
func (s *Service) Stop(id string) error {
	record, err := s.Get(id)
	if err != nil {
		return err
	}
	if record.Status.Terminal() {
		return fmt.Errorf("%w: %s is %s", ErrFinished, id, record.Status)
	}
	if record.PGID <= 0 {
		return fmt.Errorf("%w: %s has no process group yet", ErrFinished, id)
	}
	stopping := record
	stopping.Status = StatusStopping
	s.appendIfLegal(record, stopping)
	signalGroup(record.PGID)

	latest, err := s.get(id)
	if err != nil || latest.Status.Terminal() {
		return err
	}
	stopped := latest
	stopped.Status = StatusCanceled
	stopped.Error = "stopped"
	stopped.EndedAtMs = s.options.Now().UnixMilli()
	if canTransition(latest.Status, stopped.Status) {
		_ = s.store.Append(stopped)
	}
	return nil
}

// Close stops every task this process started and is still running.
//
// It is what keeps a background command from becoming an orphan: there is no
// daemon to hand it to, so the honest end of the process is the end of its
// tasks. Rows are written rather than deleted, because "canceled because pi
// exited" is a fact a later session may want to see.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	all, err := s.store.Load()
	if err != nil {
		return err
	}
	var firstErr error
	for _, record := range all {
		if !record.Status.Active() || record.OwnerPID != s.options.PID {
			continue
		}
		if record.PGID > 0 {
			signalGroup(record.PGID)
		}
		done := record
		done.Status = StatusCanceled
		done.Error = "stopped because the process exited"
		done.EndedAtMs = s.options.Now().UnixMilli()
		if err := s.store.Append(done); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Path reports where the records live, for a message that has to name it.
func (s *Service) Path() string { return s.store.Path() }

// Now is the clock the service stamps records with. Callers that print an age
// ask it rather than time.Now, so a test that moves the clock sees the same
// clock in the listing.
func (s *Service) Now() time.Time { return s.options.Now() }

func (s *Service) get(id string) (Task, error) {
	all, err := s.store.Load()
	if err != nil {
		return Task{}, err
	}
	for _, record := range all {
		if record.ID == id {
			return record, nil
		}
	}
	return Task{}, ErrNotFound
}

// appendIfLegal writes next only when the status machine allows the step. A
// refused step is not an error the caller can act on: it means another writer
// got there first, and that writer's row is the newer truth.
func (s *Service) appendIfLegal(current, next Task) {
	if !canTransition(current.Status, next.Status) {
		return
	}
	_ = s.store.Append(next)
}

func containsStatus(statuses []Status, want Status) bool {
	for _, status := range statuses {
		if status == want {
			return true
		}
	}
	return false
}

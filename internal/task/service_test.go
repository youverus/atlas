package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/tool"
)

// fixedClock is a clock a test can move.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time          { return c.now }
func (c *fixedClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// newService returns a service over a temporary directory with a clock that
// starts at a round number and a session the test controls.
func newService(t *testing.T, session string) (*Service, *fixedClock) {
	t.Helper()
	clock := &fixedClock{now: time.UnixMilli(1_000_000)}
	// Captured once: t.TempDir() answers a new directory on every call, and a
	// service that wrote to one and read from another would find nothing.
	dir := t.TempDir()
	outputs := t.TempDir()
	service := New(Options{
		Dir:          func() string { return dir },
		OwnerSession: func() string { return session },
		PID:          4242,
		Now:          clock.Now,
		Create: func(prefix string) (*os.File, error) {
			return os.CreateTemp(outputs, prefix+"*.log")
		},
	})
	return service, clock
}

func TestRegisterWritesQueuedRowAndOpensOutput(t *testing.T) {
	service, _ := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "make test", Description: "make test", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if output == nil {
		t.Fatal("expected an output file")
	}
	defer func() { _ = output.Close() }()

	record, err := service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.Status != StatusQueued {
		t.Errorf("status: got %s", record.Status)
	}
	if record.OwnerSessionID != "session-a" || record.OwnerPID != 4242 {
		t.Errorf("owner: %+v", record)
	}
	if record.OutputPath != output.Name() {
		t.Errorf("output path: got %q, want %q", record.OutputPath, output.Name())
	}
	if record.Kind != KindBash {
		t.Errorf("kind: got %s", record.Kind)
	}
}

func TestRegisterSurvivesAnUnwritableOutput(t *testing.T) {
	dir := t.TempDir()
	service := New(Options{
		Dir: func() string { return dir },
		PID: 4242,
		Create: func(string) (*os.File, error) {
			return nil, errors.New("no space")
		},
	})
	id, output, err := service.Register(tool.Background{Command: "true"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if output != nil {
		t.Fatal("expected no output file")
	}
	record, err := service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Losing the file must not lose the record: the command still ran, and the
	// only way to say so is the row.
	if record.OutputPath != "" {
		t.Errorf("output path: got %q", record.OutputPath)
	}
}

func TestStartedAndSettledSucceed(t *testing.T) {
	service, clock := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "sleep 1"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = output.Close() }()

	clock.advance(time.Second)
	service.Started(id, 99)
	record, err := service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.Status != StatusRunning || record.PGID != 99 {
		t.Fatalf("after start: %+v", record)
	}

	clock.advance(2 * time.Second)
	service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: clock.Now()})
	record, err = service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.Status != StatusSucceeded {
		t.Fatalf("status: got %s", record.Status)
	}
	if record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("exit code: %+v", record.ExitCode)
	}
	if record.EndedAtMs != clock.Now().UnixMilli() {
		t.Errorf("end time: got %d", record.EndedAtMs)
	}
}

func TestSettledRecordsFailures(t *testing.T) {
	cases := []struct {
		name    string
		exit    tool.BackgroundExit
		status  Status
		message string
	}{
		{"nonzero", tool.BackgroundExit{Code: 3}, StatusFailed, ""},
		{"timedout", tool.BackgroundExit{Code: 137, TimedOut: true}, StatusFailed, "timed out"},
		{"aborted", tool.BackgroundExit{Code: 130, Aborted: true}, StatusCanceled, ""},
		{"error", tool.BackgroundExit{Err: errors.New("exec: not found")}, StatusFailed, "exec: not found"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, clock := newService(t, "session-a")
			id, output, err := service.Register(tool.Background{Command: "x"})
			if err != nil {
				t.Fatalf("register: %v", err)
			}
			defer func() { _ = output.Close() }()
			service.Started(id, 1)
			exit := test.exit
			exit.EndedAt = clock.Now()
			service.Settled(id, exit)

			record, err := service.Get(id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if record.Status != test.status {
				t.Fatalf("status: got %s, want %s", record.Status, test.status)
			}
			if test.message != "" && record.Error != test.message {
				t.Errorf("error: got %q, want %q", record.Error, test.message)
			}
		})
	}
}

func TestSettledRefusesToLeaveATerminalStatus(t *testing.T) {
	service, clock := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = output.Close() }()
	service.Started(id, 1)
	service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: clock.Now()})

	// The late report of a killed group must not turn a finished task back into
	// a different answer.
	service.Settled(id, tool.BackgroundExit{Code: 1, EndedAt: clock.Now()})
	record, err := service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.Status != StatusSucceeded {
		t.Fatalf("status: got %s", record.Status)
	}
}

func TestSettledAfterStopKeepsCanceled(t *testing.T) {
	service, clock := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = output.Close() }()
	service.Started(id, 1)
	stopping := Task{ID: id, Status: StatusStopping}
	current, err := service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	stopping.CreatedAtMs = current.CreatedAtMs
	service.appendIfLegal(current, stopping)

	service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: clock.Now()})
	record, err := service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The process did end successfully, but it ended because it was stopped,
	// and "succeeded" is the wrong answer to why.
	if record.Status != StatusCanceled {
		t.Fatalf("status: got %s", record.Status)
	}
}

func TestQueryFiltersBySessionAndStatus(t *testing.T) {
	service, clock := newService(t, "session-a")
	mine, mineOutput, err := service.Register(tool.Background{Command: "a"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = mineOutput.Close() }()
	theirs := New(Options{
		Dir:          func() string { return filepath.Dir(service.Path()) },
		OwnerSession: func() string { return "session-b" },
		PID:          4242,
		Now:          clock.Now,
	})
	other, otherOutput, err := theirs.Register(tool.Background{Command: "b"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = otherOutput.Close() }()

	// The service under test reads the same file as theirs, which is what a
	// second process would do.
	sameFile := New(Options{
		Dir:          func() string { return filepath.Dir(service.Path()) },
		OwnerSession: func() string { return "session-a" },
		PID:          4242,
		Now:          clock.Now,
	})
	found, err := sameFile.Query(Filter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(found) != 1 || found[0].ID != mine {
		t.Fatalf("session filter: got %+v", found)
	}
	all, err := sameFile.Query(Filter{AllSessions: true})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all sessions: got %d", len(all))
	}
	active, err := sameFile.Query(Filter{Statuses: []Status{StatusRunning}})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("status filter: got %d", len(active))
	}
	queued, err := sameFile.Query(Filter{Statuses: []Status{StatusQueued}})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(queued) != 1 || queued[0].ID != mine {
		t.Fatalf("queued filter: got %+v", queued)
	}
	if other == "" {
		t.Fatal("expected an id for the other session's task")
	}
}

func TestQueryReportsAnotherProcessAsLostWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	creator := New(Options{
		Dir:          func() string { return dir },
		OwnerSession: func() string { return "session-a" },
		PID:          4242,
	})
	id, output, err := creator.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = output.Close() }()
	creator.Started(id, 4242)

	// A pid that cannot exist, standing in for a process that died mid-task.
	reader := New(Options{
		Dir:          func() string { return dir },
		OwnerSession: func() string { return "session-a" },
		PID:          4_000_000,
	})
	found, err := reader.Query(Filter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(found) != 1 || found[0].Status != StatusLost {
		t.Fatalf("want lost, got %+v", found)
	}
	if found[0].Error == "" {
		t.Error("expected an explanation")
	}
	// The file still says what happened, because a process that is merely
	// suspended must not lose its task to a reader's opinion.
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), `"status":"lost"`) {
		t.Error("lost was written back to the record file")
	}
}

func TestGetReportsAnUnknownID(t *testing.T) {
	service, _ := newService(t, "session-a")
	if _, err := service.Get("bg_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestReadReturnsSlicesAndOffset(t *testing.T) {
	service, clock := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := output.WriteString("hello world"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	service.Started(id, 1)

	first, err := service.Read(id, 0, 5)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if first.Content != "hello" {
		t.Errorf("content: got %q", first.Content)
	}
	if first.TotalBytes != int64(len("hello world")) {
		t.Errorf("total: got %d", first.TotalBytes)
	}
	if first.NextOffset != 5 {
		t.Errorf("next offset: got %d", first.NextOffset)
	}
	// A running task is not done even at the end of the file: more can arrive.
	if first.Done {
		t.Error("a running task was reported done")
	}

	rest, err := service.Read(id, first.NextOffset, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rest.Content != " world" {
		t.Errorf("rest: got %q", rest.Content)
	}
	if rest.Done {
		t.Error("a running task was reported done at the end of its file")
	}

	service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: clock.Now()})
	finished, err := service.Read(id, 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if finished.Content != "hello world" {
		t.Errorf("finished: got %q", finished.Content)
	}
	if !finished.Done {
		t.Error("a finished task read from the start was not reported done")
	}
}

func TestReadWithoutAnOutputFile(t *testing.T) {
	dir := t.TempDir()
	service := New(Options{
		Dir: func() string { return dir },
		PID: 4242,
	})
	id, _, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	read, err := service.Read(id, 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Content != "" || read.Done {
		t.Fatalf("unexpected read: %+v", read)
	}
	service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: time.Now()})
	read, err = service.Read(id, 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !read.Done {
		t.Error("a finished task without output was not reported done")
	}
}

func TestStopWithoutAProcessGroupIsRefused(t *testing.T) {
	service, _ := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = output.Close() }()
	// Queued means the process never started, so there is no group to signal.
	if err := service.Stop(id); !errors.Is(err, ErrFinished) {
		t.Fatalf("want ErrFinished, got %v", err)
	}
}

func TestStopRefusesAFinishedTask(t *testing.T) {
	service, clock := newService(t, "session-a")
	id, output, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = output.Close() }()
	service.Started(id, 1)
	service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: clock.Now()})
	if err := service.Stop(id); !errors.Is(err, ErrFinished) {
		t.Fatalf("want ErrFinished, got %v", err)
	}
}

func TestStopReportsAnUnknownID(t *testing.T) {
	service, _ := newService(t, "session-a")
	if err := service.Stop("bg_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCloseCancelsThisProcessTasksOnly(t *testing.T) {
	dir := t.TempDir()
	service := New(Options{
		Dir:          func() string { return dir },
		OwnerSession: func() string { return "session-a" },
		PID:          4242,
	})
	mine, mineOutput, err := service.Register(tool.Background{Command: "x"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = mineOutput.Close() }()
	service.Started(mine, 4242)

	other := New(Options{
		Dir:          func() string { return dir },
		OwnerSession: func() string { return "session-b" },
		PID:          9999,
	})
	theirs, theirsOutput, err := other.Register(tool.Background{Command: "y"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = theirsOutput.Close() }()
	other.Started(theirs, 9999)

	if err := service.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	mineRecord, err := service.Get(mine)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if mineRecord.Status != StatusCanceled {
		t.Errorf("own task: got %s", mineRecord.Status)
	}
	if mineRecord.Error == "" {
		t.Error("expected the row to say why it was canceled")
	}
	theirsRecord, err := other.Get(theirs)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Another process's task is not this process's to end.
	if theirsRecord.Status != StatusRunning {
		t.Errorf("other task: got %s", theirsRecord.Status)
	}
	// Closing twice is not an error: a shutdown path should not have to know
	// whether something else already closed it.
	if err := service.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestRegisterRefusesAfterClose(t *testing.T) {
	service, _ := newService(t, "session-a")
	if err := service.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := service.Register(tool.Background{Command: "x"}); err == nil {
		t.Fatal("expected an error after close")
	}
}

func TestNewDefaultsTheProcessAndClock(t *testing.T) {
	service := New(Options{Dir: func() string { return t.TempDir() }})
	if service.options.PID != os.Getpid() {
		t.Errorf("pid: got %d", service.options.PID)
	}
	if service.options.OwnerSession() != "" {
		t.Error("expected an empty owner session")
	}
	if service.Now().IsZero() {
		t.Error("expected a clock")
	}
}

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"pi-go/internal/task"
	"pi-go/internal/tool"
)

// harness is a service and the tools over it, plus the pieces a test needs to
// move a task through its life.
type harness struct {
	t       *testing.T
	service *task.Service
	tools   map[string]tool.Tool
	now     time.Time
}

func newHarness(t *testing.T, session string) *harness {
	t.Helper()
	dir := t.TempDir()
	outputs := t.TempDir()
	service := task.New(task.Options{
		// Captured: t.TempDir() answers a new directory on every call, and a
		// service that wrote to one and read from another would find nothing.
		Dir:          func() string { return dir },
		OwnerSession: func() string { return session },
		PID:          4242,
		Now:          func() time.Time { return time.UnixMilli(1_000_000) },
		Create: func(prefix string) (*os.File, error) {
			return os.CreateTemp(outputs, prefix+"*.log")
		},
	})
	built := Tools(service)
	set := make(map[string]tool.Tool, len(built))
	for _, impl := range built {
		set[impl.Name()] = impl
	}
	return &harness{t: t, service: service, tools: set, now: time.UnixMilli(1_000_000)}
}

// start registers a task and returns its id and output file, which the test
// writes to when it wants output.
func (h *harness) start(command string) (string, *os.File) {
	h.t.Helper()
	id, output, err := h.service.Register(tool.Background{Command: command, Description: command})
	if err != nil {
		h.t.Fatalf("register: %v", err)
	}
	return id, output
}

func (h *harness) call(name, args string) (tool.Result, error) {
	h.t.Helper()
	impl, ok := h.tools[name]
	if !ok {
		h.t.Fatalf("%s is not registered", name)
	}
	return impl.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      name,
		Arguments: json.RawMessage(args),
	}, func(tool.Partial) {})
}

func (h *harness) text(name, args string) string {
	h.t.Helper()
	result, err := h.call(name, args)
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		h.t.Fatalf("%s: unexpected content %+v", name, result.Content)
	}
	return result.Content[0].Text
}

func TestToolSetAndSchemas(t *testing.T) {
	h := newHarness(t, "session-a")
	if len(h.tools) != 3 {
		t.Fatalf("want three tools, got %d", len(h.tools))
	}
	var query struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(h.tools["task_query"].Parameters(), &query); err != nil {
		t.Fatalf("task_query schema: %v", err)
	}
	for _, field := range []string{"id", "status", "all_sessions"} {
		if _, ok := query.Properties[field]; !ok {
			t.Errorf("task_query is missing %s", field)
		}
	}
	var output struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(h.tools["task_output"].Parameters(), &output); err != nil {
		t.Fatalf("task_output schema: %v", err)
	}
	// offset and wait_ms have defaults, so only the id is required.
	if len(output.Required) != 1 || output.Required[0] != "id" {
		t.Errorf("task_output required: %v", output.Required)
	}
	for _, field := range []string{"id", "offset", "wait_ms"} {
		if _, ok := output.Properties[field]; !ok {
			t.Errorf("task_output is missing %s", field)
		}
	}
}

func TestQueryWithNothingIsNotAnError(t *testing.T) {
	h := newHarness(t, "session-a")
	got := h.text("task_query", `{}`)
	if !strings.Contains(got, "No tasks") {
		t.Fatalf("unexpected answer: %q", got)
	}
	if !strings.Contains(got, "run_in_background") {
		t.Errorf("the answer does not say how a task is started: %q", got)
	}
}

func TestQueryListsThisSessionOnly(t *testing.T) {
	h := newHarness(t, "session-a")
	mine, mineOutput := h.start("make test")
	defer func() { _ = mineOutput.Close() }()
	h.service.Started(mine, 1)

	other := task.New(task.Options{
		Dir:          func() string { return strings.TrimSuffix(h.service.Path(), task.FileName) },
		OwnerSession: func() string { return "session-b" },
		PID:          9999,
	})
	theirs, theirsOutput, err := other.Register(tool.Background{Command: "their command"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = theirsOutput.Close() }()

	got := h.text("task_query", `{}`)
	if !strings.Contains(got, mine) || !strings.Contains(got, "make test") {
		t.Fatalf("own task missing: %s", got)
	}
	if strings.Contains(got, theirs) {
		t.Fatalf("another session's task leaked into the listing: %s", got)
	}

	all := h.text("task_query", `{"all_sessions":true}`)
	if !strings.Contains(all, theirs) {
		t.Fatalf("all_sessions did not include the other task: %s", all)
	}
}

func TestQueryShowsOneTaskInFull(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("npm run dev")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 7)
	h.service.Settled(id, tool.BackgroundExit{
		Code:      2,
		EndedAt:   h.now.Add(3 * time.Second),
		StartedAt: h.now,
	})

	got := h.text("task_query", `{"id":"`+id+`"}`)
	for _, want := range []string{id, "failed", "npm run dev", "Command:", "Exit: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail is missing %q:\n%s", want, got)
		}
	}
}

func TestQueryUnknownIDIsAnError(t *testing.T) {
	h := newHarness(t, "session-a")
	_, err := h.call("task_query", `{"id":"bg_nope"}`)
	if !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestQueryStatusFilter(t *testing.T) {
	h := newHarness(t, "session-a")
	queued, queuedOutput := h.start("one")
	defer func() { _ = queuedOutput.Close() }()
	running, runningOutput := h.start("two")
	defer func() { _ = runningOutput.Close() }()
	h.service.Started(running, 1)
	finished, finishedOutput := h.start("three")
	defer func() { _ = finishedOutput.Close() }()
	h.service.Started(finished, 2)
	h.service.Settled(finished, tool.BackgroundExit{Code: 0, EndedAt: h.now})

	active := h.text("task_query", `{"status":"active"}`)
	if !strings.Contains(active, queued) || !strings.Contains(active, running) {
		t.Fatalf("active filter lost a task:\n%s", active)
	}
	if strings.Contains(active, finished) {
		t.Fatalf("active filter kept a finished task:\n%s", active)
	}
	done := h.text("task_query", `{"status":"finished"}`)
	if !strings.Contains(done, finished) || strings.Contains(done, running) {
		t.Fatalf("finished filter:\n%s", done)
	}
	succeeded := h.text("task_query", `{"status":"succeeded"}`)
	if !strings.Contains(succeeded, finished) || strings.Contains(succeeded, queued) {
		t.Fatalf("single status filter:\n%s", succeeded)
	}
	if _, err := h.call("task_query", `{"status":"nonsense"}`); err == nil {
		t.Fatal("an unknown status was accepted")
	}
}

func TestOutputReadsAndContinues(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("build")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 1)
	if _, err := output.WriteString("first line\nsecond line\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := output.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := h.text("task_output", `{"id":"`+id+`"}`)
	if !strings.Contains(got, "first line") || !strings.Contains(got, "second line") {
		t.Fatalf("output missing:\n%s", got)
	}
	if !strings.Contains(got, "running") {
		t.Errorf("the reply does not report the state:\n%s", got)
	}
	if !strings.Contains(got, "next offset") {
		t.Errorf("the reply does not say how to continue:\n%s", got)
	}

	// A second read from the end reports no new output but still says where it
	// stands, which is what makes polling useful.
	offset := len("first line\nsecond line\n")
	again := h.text("task_output", `{"id":"`+id+`","offset":`+strconv.Itoa(offset)+`}`)
	if strings.Contains(again, "first line") {
		t.Fatalf("the offset replayed old output:\n%s", again)
	}
	if !strings.Contains(again, "(no output yet)") {
		t.Fatalf("unexpected empty read:\n%s", again)
	}
}

func TestOutputReportsCompletion(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("build")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 1)
	if _, err := output.WriteString("done\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	h.service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: h.now})

	got := h.text("task_output", `{"id":"`+id+`"}`)
	if !strings.Contains(got, "complete") {
		t.Fatalf("a finished task does not report completion:\n%s", got)
	}
	if !strings.Contains(got, "succeeded") {
		t.Fatalf("the status is missing:\n%s", got)
	}
}

func TestOutputOfATaskWithNoOutput(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("quiet")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 1)
	h.service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: h.now})
	got := h.text("task_output", `{"id":"`+id+`"}`)
	if !strings.Contains(got, "(no output)") {
		t.Fatalf("unexpected answer:\n%s", got)
	}
}

func TestOutputWaitsForNewOutput(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("slow")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 1)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = output.WriteString("arrived\n")
		_ = output.Sync()
	}()

	started := time.Now()
	got := h.text("task_output", `{"id":"`+id+`","wait_ms":5000}`)
	if !strings.Contains(got, "arrived") {
		t.Fatalf("the wait did not return the new output:\n%s", got)
	}
	// The wait must end when the output arrives rather than at the deadline.
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the wait ran to the deadline: %v", elapsed)
	}
}

func TestOutputRejectsBadArguments(t *testing.T) {
	h := newHarness(t, "session-a")
	if _, err := h.call("task_output", `{}`); err == nil {
		t.Error("a missing id was accepted")
	}
	if _, err := h.call("task_output", `{"id":"bg_x","offset":-1}`); err == nil {
		t.Error("a negative offset was accepted")
	}
	if _, err := h.call("task_output", `{"id":"bg_x","wait_ms":-1}`); err == nil {
		t.Error("a negative wait was accepted")
	}
	id, output := h.start("x")
	defer func() { _ = output.Close() }()
	if _, err := h.call("task_output", `{"id":"`+id+`","offset":9999}`); err != nil {
		t.Errorf("a past-the-end offset is a clamp, not an error: %v", err)
	}
}

func TestStopCancelsARunningTask(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("sleep 100")
	defer func() { _ = output.Close() }()
	// A group id that cannot exist: the signal is harmless, and what the test
	// asserts is the record that follows it.
	h.service.Started(id, 3_000_000)

	got := h.text("task_stop", `{"id":"`+id+`"}`)
	if !strings.Contains(got, "canceled") {
		t.Fatalf("the task was not canceled:\n%s", got)
	}
	record, err := h.service.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.Status != task.StatusCanceled {
		t.Fatalf("status: got %s", record.Status)
	}
	// Its output stays readable, which is why stopping is not deleting.
	if _, err := h.call("task_output", `{"id":"`+id+`"}`); err != nil {
		t.Fatalf("output after stop: %v", err)
	}
}

func TestStopOnAFinishedTaskIsNotAnError(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("true")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 1)
	h.service.Settled(id, tool.BackgroundExit{Code: 0, EndedAt: h.now})

	got := h.text("task_stop", `{"id":"`+id+`"}`)
	if !strings.Contains(got, "Nothing to stop") || !strings.Contains(got, "succeeded") {
		t.Fatalf("unexpected answer:\n%s", got)
	}
}

func TestStopRejectsBadArguments(t *testing.T) {
	h := newHarness(t, "session-a")
	if _, err := h.call("task_stop", `{}`); err == nil {
		t.Error("a missing id was accepted")
	}
	if _, err := h.call("task_stop", `{"id":"bg_nope"}`); !errors.Is(err, task.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestListingCountsActiveTasks(t *testing.T) {
	h := newHarness(t, "session-a")
	running, runningOutput := h.start("one")
	defer func() { _ = runningOutput.Close() }()
	h.service.Started(running, 1)
	finished, finishedOutput := h.start("two")
	defer func() { _ = finishedOutput.Close() }()
	h.service.Started(finished, 2)
	h.service.Settled(finished, tool.BackgroundExit{Code: 0, EndedAt: h.now})

	got := h.text("task_query", `{}`)
	if !strings.Contains(got, "2 task(s), 1 active") {
		t.Fatalf("unexpected summary:\n%s", got)
	}
}

func TestListingShowsAFailedExitCode(t *testing.T) {
	h := newHarness(t, "session-a")
	id, output := h.start("compile")
	defer func() { _ = output.Close() }()
	h.service.Started(id, 1)
	h.service.Settled(id, tool.BackgroundExit{Code: 3, EndedAt: h.now})
	got := h.text("task_query", `{}`)
	if !strings.Contains(got, "(exit 3)") {
		t.Fatalf("the listing hides the exit code:\n%s", got)
	}
}

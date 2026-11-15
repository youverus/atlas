//go:build unix

package shell

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"pi-go/internal/tool"
)

// fakeRecorder records what the tool reported, so a test can assert the order of
// the three calls without running a task service.
type fakeRecorder struct {
	mu      sync.Mutex
	specs   map[string]tool.Background
	starts  map[string]int
	settles map[string]tool.BackgroundExit
	// outputs is where each run's file went, so a test can read what the
	// command printed without guessing a path.
	outputs map[string]string
	order   []string
	// dir holds those files, so a test that starts nothing leaks nothing.
	dir      string
	onSettle chan tool.BackgroundExit
}

func newFakeRecorder(t *testing.T) *fakeRecorder {
	t.Helper()
	return &fakeRecorder{
		specs:    map[string]tool.Background{},
		starts:   map[string]int{},
		settles:  map[string]tool.BackgroundExit{},
		outputs:  map[string]string{},
		dir:      t.TempDir(),
		onSettle: make(chan tool.BackgroundExit, 4),
	}
}

func (r *fakeRecorder) Register(spec tool.Background) (string, *os.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := "bg_test"
	r.specs[id] = spec
	r.order = append(r.order, "register")
	file, err := os.CreateTemp(r.dir, "output-*.log")
	if err != nil {
		return "", nil, err
	}
	r.outputs[id] = file.Name()
	return id, file, nil
}

func (r *fakeRecorder) Started(id string, pgid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts[id] = pgid
	r.order = append(r.order, "started")
}

func (r *fakeRecorder) Settled(id string, exit tool.BackgroundExit) {
	r.mu.Lock()
	r.settles[id] = exit
	r.order = append(r.order, "settled")
	r.mu.Unlock()
	r.onSettle <- exit
}

func (r *fakeRecorder) spec(id string) tool.Background {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.specs[id]
}

func (r *fakeRecorder) outputPath(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outputs[id]
}

func (r *fakeRecorder) pgid(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[id]
}

func (r *fakeRecorder) wait(t *testing.T) tool.BackgroundExit {
	t.Helper()
	select {
	case exit := <-r.onSettle:
		return exit
	case <-time.After(10 * time.Second):
		t.Fatal("the background command never settled")
		return tool.BackgroundExit{}
	}
}

func TestParametersWithoutARecorderAreUnchanged(t *testing.T) {
	impl := newTool(t, Options{})
	// The schema a build before background commands served, byte for byte. A
	// host with no runner must not advertise the argument.
	want := `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to execute"},
    "timeout": {"type": "number", "description": "Timeout in seconds (optional, no default timeout)"}
  },
  "required": ["command"]
}`
	if got := string(impl.Parameters()); got != want {
		t.Fatalf("schema changed without a recorder:\n%s", got)
	}
	if got := impl.Description(); got != bashDescription {
		t.Fatalf("description changed without a recorder:\n%s", got)
	}
}

func TestParametersWithARecorderOfferBackground(t *testing.T) {
	impl := newTool(t, Options{Background: newFakeRecorder(t)})
	var decoded struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(impl.Parameters(), &decoded); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if decoded.Properties["run_in_background"].Type != "boolean" {
		t.Fatalf("run_in_background missing: %s", impl.Parameters())
	}
	// Still one required argument: a command can ask to be detached without
	// saying so, and the default has to stay the blocking call.
	if len(decoded.Required) != 1 || decoded.Required[0] != "command" {
		t.Fatalf("required: %v", decoded.Required)
	}
	if !strings.Contains(impl.Description(), "run_in_background") {
		t.Fatalf("description does not mention the argument: %s", impl.Description())
	}
}

func TestForegroundIsTheDefault(t *testing.T) {
	recorder := newFakeRecorder(t)
	impl := newTool(t, Options{Cwd: t.TempDir(), Background: recorder})
	result := run(t, impl, `{"command":"echo foreground"}`)
	if got := text(t, result); !strings.Contains(got, "foreground") {
		t.Fatalf("unexpected output: %q", got)
	}
	// Nothing touched the recorder, which is what keeps a plain command from
	// appearing in the task list.
	if len(recorder.order) != 0 {
		t.Fatalf("foreground call reported to the recorder: %v", recorder.order)
	}
}

func TestBackgroundStartsDetachedAndReportsTheID(t *testing.T) {
	recorder := newFakeRecorder(t)
	impl := newTool(t, Options{Cwd: t.TempDir(), Background: recorder})
	result := run(t, impl, `{"command":"echo detached","run_in_background":true}`)
	body := text(t, result)
	if !strings.Contains(body, "bg_test") {
		t.Fatalf("result does not name the task: %s", body)
	}

	exit := recorder.wait(t)
	if exit.Err != nil {
		t.Fatalf("the command failed: %v", exit.Err)
	}
	if exit.Code != 0 {
		t.Fatalf("exit code: %d", exit.Code)
	}
	if exit.TimedOut {
		t.Error("a fast command timed out")
	}
	// The output went to the task's file, not to the result.
	spec := recorder.spec("bg_test")
	if spec.Command != "echo detached" {
		t.Errorf("recorded command: %q", spec.Command)
	}
	if spec.Description != "echo detached" {
		t.Errorf("recorded description: %q", spec.Description)
	}
	if spec.ToolCallID != "call-1" {
		t.Errorf("recorded tool call: %q", spec.ToolCallID)
	}
	pgid := recorder.pgid("bg_test")
	if pgid <= 1 {
		t.Fatalf("no process group was recorded: %d", pgid)
	}
	if got := recorder.order; len(got) != 3 || got[0] != "register" || got[1] != "started" || got[2] != "settled" {
		t.Fatalf("unexpected call order: %v", got)
	}

	contents, err := os.ReadFile(recorder.outputPath("bg_test"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(contents), "detached") {
		t.Fatalf("output file does not hold the output: %q", contents)
	}
}

func TestBackgroundStopsItselfAtTheDeadline(t *testing.T) {
	recorder := newFakeRecorder(t)
	impl := newTool(t, Options{Cwd: t.TempDir(), Background: recorder})
	run(t, impl, `{"command":"sleep 30","timeout":1,"run_in_background":true}`)
	exit := recorder.wait(t)
	if !exit.TimedOut {
		t.Fatalf("want a timeout, got %+v", exit)
	}
}

func TestBackgroundWithoutARecorderIsRefused(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	err := runError(t, impl, `{"command":"true","run_in_background":true}`)
	if !strings.Contains(err.Error(), "background") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBackgroundSurvivesTheCallContext(t *testing.T) {
	recorder := newFakeRecorder(t)
	impl := newTool(t, Options{Cwd: t.TempDir(), Background: recorder})
	// The context is cancelled the moment Execute returns, which is what a
	// finished turn does. The command must not notice.
	ctx, cancel := context.WithCancel(context.Background())
	_, err := impl.Execute(ctx, tool.Call{
		ID:        "call-1",
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"sleep 0.4; echo alive","run_in_background":true}`),
	}, func(tool.Partial) {})
	cancel()
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	exit := recorder.wait(t)
	if exit.Err != nil {
		t.Fatalf("the command was cut short by the cancelled context: %v", exit.Err)
	}
	if exit.Code != 0 || exit.TimedOut {
		t.Fatalf("unexpected exit: %+v", exit)
	}
}

func TestDescribeCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"make test", "make test"},
		{"  make test  ", "make test"},
		{"one\ntwo", "one"},
		{"", "(empty command)"},
		{"   ", "(empty command)"},
	}
	for _, test := range cases {
		if got := describeCommand(test.in); got != test.want {
			t.Errorf("describeCommand(%q): got %q, want %q", test.in, got, test.want)
		}
	}
	long := strings.Repeat("a", 200)
	if got := describeCommand(long); len(got) != 83 || !strings.HasSuffix(got, "...") {
		t.Errorf("long command: got %d characters %q", len(got), got)
	}
	// The cut has to land on a rune boundary, because the listing is text and
	// half a character is a rendering bug a reader cannot diagnose.
	multibyte := describeCommand(strings.Repeat("汉", 40))
	if !strings.HasSuffix(multibyte, "...") {
		t.Fatalf("multibyte command was not shortened: %q", multibyte)
	}
	if !utf8.ValidString(multibyte) {
		t.Fatalf("the cut split a character: %q", multibyte)
	}
}

func TestDurationText(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Hour, "1 hour"},
		{2 * time.Hour, "2 hours"},
		{time.Minute, "1 minute"},
		{90 * time.Second, "90 seconds"},
		{time.Second, "1 second"},
	}
	for _, test := range cases {
		if got := durationText(test.in); got != test.want {
			t.Errorf("durationText(%v): got %q, want %q", test.in, got, test.want)
		}
	}
}

func TestWaitDetachedReportsASignal(t *testing.T) {
	// A group killed from outside must report the 128+signal convention rather
	// than a plain success, because the caller reads the code to decide whether
	// the command worked.
	recorder := newFakeRecorder(t)
	impl := newTool(t, Options{Cwd: t.TempDir(), Background: recorder})
	run(t, impl, `{"command":"sleep 30","run_in_background":true}`)
	pgid := recorder.pgid("bg_test")
	if pgid <= 1 {
		t.Fatalf("no process group: %d", pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}
	exit := recorder.wait(t)
	if exit.Code < 128 {
		t.Fatalf("want a signal exit code, got %+v", exit)
	}
}

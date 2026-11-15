package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
)

// run executes the tool with a context and returns the result.
func run(t *testing.T, impl tool.Tool, args string) tool.Result {
	t.Helper()
	result, err := impl.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      impl.Name(),
		Arguments: json.RawMessage(args),
	}, func(tool.Partial) {})
	if err != nil {
		t.Fatalf("bash returned error: %v", err)
	}
	return result
}

// runError executes the tool and returns the Go error it produced. Spawn
// failures and bad arguments are errors; a failing command is a result.
func runError(t *testing.T, impl tool.Tool, args string) error {
	t.Helper()
	_, err := impl.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      impl.Name(),
		Arguments: json.RawMessage(args),
	}, func(tool.Partial) {})
	if err == nil {
		t.Fatalf("bash was expected to fail with %s", args)
	}
	return err
}

func text(t *testing.T, result tool.Result) string {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	return result.Content[0].Text
}

func newTool(t *testing.T, options Options) tool.Tool {
	t.Helper()
	tools := Tools(options)
	if len(tools) != 1 {
		t.Fatalf("expected one shell tool, got %d", len(tools))
	}
	return tools[0]
}

func TestBashCapturesOutput(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})

	result := run(t, impl, `{"command":"echo hello"}`)
	// The trailing newline is kept: truncation only rewrites content when it
	// actually cuts something.
	if got := text(t, result); got != "hello\n" {
		t.Fatalf("output = %q", got)
	}
	if result.IsError {
		t.Fatal("a successful command must not be an error result")
	}

	var structured bashStructuredContent
	if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
		t.Fatalf("structuredContent: %v", err)
	}
	if structured.Output != "hello\n" || structured.ExitCode != 0 || structured.Truncated {
		t.Fatalf("structuredContent = %+v", structured)
	}
	if structured.WallTimeSeconds < 0 {
		t.Fatalf("wall time = %v", structured.WallTimeSeconds)
	}
	if result.Details != nil {
		t.Fatalf("an untruncated command should not set details: %s", result.Details)
	}
}

// stdout and stderr share one stream, so the combined output keeps the order a
// terminal would show.
func TestBashMergesStdoutAndStderr(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	result := run(t, impl, `{"command":"echo out; echo err 1>&2; echo out2"}`)

	got := text(t, result)
	for _, want := range []string{"out", "err", "out2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged output is missing %q: %q", want, got)
		}
	}
}

func TestBashEmptyOutput(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	if got := text(t, run(t, impl, `{"command":"true"}`)); got != "(no output)" {
		t.Fatalf("output = %q", got)
	}
}

func TestBashNonZeroExit(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	result := run(t, impl, `{"command":"echo failing; exit 3"}`)

	if !result.IsError {
		t.Fatal("a non-zero exit must be an error result")
	}
	if got := text(t, result); got != "failing\n\n\nCommand exited with code 3" {
		t.Fatalf("output = %q", got)
	}

	var structured bashStructuredContent
	if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
		t.Fatalf("structuredContent: %v", err)
	}
	if structured.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", structured.ExitCode)
	}
}

func TestBashRunsInCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("found"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	impl := newTool(t, Options{Cwd: dir})

	if got := text(t, run(t, impl, `{"command":"cat marker.txt"}`)); got != "found" {
		t.Fatalf("output = %q, want the marker file content", got)
	}
}

func TestBashExtraEnvironment(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir(), Env: map[string]string{"PI_MODEL": "test-model"}})

	if got := text(t, run(t, impl, `{"command":"echo $PI_MODEL"}`)); got != "test-model\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestBashCommandPrefix(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir(), CommandPrefix: "GREETING=hi"})

	if got := text(t, run(t, impl, `{"command":"echo $GREETING"}`)); got != "hi\n" {
		t.Fatalf("output = %q", got)
	}
}

// Long output keeps its tail and the full text lands in a temp file the model
// can read.
func TestBashTruncatesTailAndSavesFullOutput(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	result := run(t, impl, `{"command":"seq 1 2500"}`)
	got := text(t, result)

	var details bashDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.Truncation == nil || details.FullOutputPath == "" {
		t.Fatalf("details = %+v", details)
	}
	truncation := details.Truncation
	if truncation.TotalLines != 2500 || truncation.OutputLines != 2000 || truncation.TruncatedBy != "lines" {
		t.Fatalf("truncation = %+v", truncation)
	}

	notice := fmt.Sprintf("[Showing lines 501-2500 of 2500. Full output: %s]", details.FullOutputPath)
	if !strings.HasSuffix(got, notice) {
		t.Fatalf("missing tail notice:\n%s\nwant suffix:\n%s", got, notice)
	}
	if !strings.HasPrefix(got, "501\n") {
		t.Fatalf("output should start at line 501: %q", got[:min(20, len(got))])
	}

	full, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatalf("read full output: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(details.FullOutputPath) })
	if lines := strings.Count(string(full), "\n"); lines != 2500 {
		t.Fatalf("full output has %d lines, want 2500", lines)
	}

	var structured bashStructuredContent
	if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
		t.Fatalf("structuredContent: %v", err)
	}
	// The 10895-byte output fits the 1MB structured budget, so structuredContent
	// carries all of it and reports no truncation. That is pi's split: the model
	// sees a tail, a programmatic caller sees everything.
	if structured.Truncated || structured.FullOutputPath != "" {
		t.Fatalf("structuredContent should be complete, got truncated=%v path=%q",
			structured.Truncated, structured.FullOutputPath)
	}
	if lines := strings.Count(structured.Output, "\n"); lines != 2500 {
		t.Fatalf("structured output has %d lines, want 2500", lines)
	}
	if !strings.HasPrefix(structured.Output, "1\n2\n") {
		t.Fatalf("structured output should keep the head: %q", structured.Output[:min(20, len(structured.Output))])
	}
}

// The file holding the untruncated output goes where the caller says, which is
// how a session's leftovers end up beside the conversation instead of in a temp
// directory nobody cleans.
func TestBashWritesTheSpillWhereTheCallerSays(t *testing.T) {
	spillDir := filepath.Join(t.TempDir(), "spill")
	impl := newTool(t, Options{Cwd: t.TempDir(), Spill: tool.NewSpill(func() string { return spillDir })})

	result := run(t, impl, `{"command":"seq 1 2500"}`)

	var details bashDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.FullOutputPath == "" {
		t.Fatal("no full output path")
	}
	if dir := filepath.Dir(details.FullOutputPath); dir != spillDir {
		t.Fatalf("spill in %q, want %q", dir, spillDir)
	}
	full, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if lines := strings.Count(string(full), "\n"); lines != 2500 {
		t.Fatalf("spill has %d lines, want 2500", lines)
	}
	// The notice the model reads has to name the same file, or the two halves
	// of "where did it go" would disagree.
	if notice := text(t, result); !strings.Contains(notice, details.FullOutputPath) {
		t.Fatalf("notice does not name the spill:\n%s", notice)
	}
}

// The completion criterion for 0.12.8: the path in the notice is readable by the
// read tool. Both halves are exercised against real files — a notice naming a
// path the file tools could not open would be worse than losing the tail, because
// the model would keep trying.
func TestBashSpillIsReadableByTheReadTool(t *testing.T) {
	cwd := t.TempDir()
	spillDir := filepath.Join(t.TempDir(), "spill")
	bash := newTool(t, Options{Cwd: cwd, Spill: tool.NewSpill(func() string { return spillDir })})

	result := run(t, bash, `{"command":"seq 1 2500"}`)
	var details bashDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}

	// The tools are built for a workspace the spill is not inside: that is the
	// interesting case, because /export, /skill and MCP all read from outside
	// cwd the same way.
	var reader tool.Tool
	for _, candidate := range fs.Tools(fs.Options{Cwd: cwd}) {
		if candidate.Name() == "read" {
			reader = candidate
		}
	}
	if reader == nil {
		t.Fatal("no read tool")
	}
	reply, err := reader.Execute(context.Background(), tool.Call{
		ID:        "call-2",
		Name:      "read",
		Arguments: json.RawMessage(fmt.Sprintf(`{"path":%q,"limit":5}`, details.FullOutputPath)),
	}, func(tool.Partial) {})
	if err != nil {
		t.Fatalf("read the spill: %v", err)
	}
	got := text(t, reply)
	if !strings.HasPrefix(got, "1\n2\n3\n4\n5\n") {
		t.Fatalf("read from the spill = %q", got[:min(40, len(got))])
	}
}

func TestBashTimeout(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	started := time.Now()
	result := run(t, impl, `{"command":"echo before; sleep 30","timeout":1}`)
	elapsed := time.Since(started)

	if !result.IsError {
		t.Fatal("a timed out command must be an error result")
	}
	if got := text(t, result); got != "before\n\n\nCommand timed out after 1 seconds" {
		t.Fatalf("output = %q", got)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout took %v, the process group was not killed", elapsed)
	}
}

// A background child inherits the output pipe. If only the shell were killed,
// the child would survive and run its follow-up command, so the marker file is
// the evidence that the whole group died.
func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	impl := newTool(t, Options{Cwd: dir})

	run(t, impl, `{"command":"( sleep 1; touch marker ) & sleep 30","timeout":1}`)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the background child survived the process group kill")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestBashAbort(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan tool.Result, 1)
	go func() {
		result, err := impl.Execute(ctx, tool.Call{
			ID:        "call-1",
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"echo starting; sleep 30"}`),
		}, func(tool.Partial) {})
		if err != nil {
			t.Errorf("bash returned error: %v", err)
			done <- tool.Result{}
			return
		}
		done <- result
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if !result.IsError {
			t.Fatal("an aborted command must be an error result")
		}
		if got := text(t, result); !strings.HasSuffix(got, "Command aborted") {
			t.Fatalf("output = %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("abort did not stop the command")
	}
}

func TestBashStreamsPartialUpdates(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir()})

	var updates int
	_, err := impl.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"echo one"}`),
	}, func(tool.Partial) { updates++ })
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// The initial empty update is always emitted so a UI can show the tool
	// before it produces output.
	if updates == 0 {
		t.Fatal("expected at least one partial update")
	}
}

func TestBashRejectsBadArguments(t *testing.T) {
	cwd := t.TempDir()

	tests := []struct {
		name     string
		args     string
		wantPart string
	}{
		{"missing command", `{}`, "command is required"},
		{"blank command", `{"command":"   "}`, "command is required"},
		{"zero timeout", `{"command":"true","timeout":0}`, "Invalid timeout: must be a finite number of seconds"},
		{"negative timeout", `{"command":"true","timeout":-5}`, "Invalid timeout: must be a finite number of seconds"},
		{"huge timeout", `{"command":"true","timeout":99999999}`, "Invalid timeout: maximum is 2147483.647 seconds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runError(t, newTool(t, Options{Cwd: cwd}), test.args)
			if !strings.Contains(err.Error(), test.wantPart) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantPart)
			}
		})
	}
}

func TestBashMissingWorkingDirectory(t *testing.T) {
	impl := newTool(t, Options{Cwd: filepath.Join(t.TempDir(), "gone")})
	err := runError(t, impl, `{"command":"true"}`)

	want := "Working directory does not exist: "
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Cannot execute bash commands.") {
		t.Fatalf("error = %q", err)
	}
}

func TestResolveTimeout(t *testing.T) {
	tests := []struct {
		name    string
		value   *tool.Number
		want    time.Duration
		wantErr bool
	}{
		{"unset", nil, 0, false},
		{"one second", numberPtr(1), time.Second, false},
		{"many seconds", numberPtr(90), 90 * time.Second, false},
		{"zero", numberPtr(0), 0, true},
		{"too large", numberPtr(maxTimeoutMs), 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveTimeout(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTimeout: %v", err)
			}
			if got != test.want {
				t.Fatalf("resolveTimeout = %v, want %v", got, test.want)
			}
		})
	}
}

// fakeSandbox records what the tool handed it and replaces the command, which is
// enough to prove the tool routes through the interface without this package
// depending on internal/sandbox.
type fakeSandbox struct {
	argv []string
	env  []string
	err  error
}

func (f *fakeSandbox) Wrap(argv []string, env []string) ([]string, []string, error) {
	f.argv = argv
	f.env = env
	if f.err != nil {
		return nil, nil, f.err
	}
	return []string{argv[0], "-c", "echo confined"}, append(env, "CONFINED=1"), nil
}

func TestBashRunsThroughSandbox(t *testing.T) {
	confine := &fakeSandbox{}
	impl := newTool(t, Options{Cwd: t.TempDir(), Sandbox: confine})

	if got := text(t, run(t, impl, `{"command":"echo original"}`)); got != "confined\n" {
		t.Fatalf("output = %q, want the sandboxed command's output", got)
	}
	if len(confine.argv) != 3 || confine.argv[2] != "echo original" {
		t.Fatalf("sandbox received %v, want the shell, -c and the original command", confine.argv)
	}
	if len(confine.env) == 0 {
		t.Fatal("sandbox received no environment")
	}
}

// A wrap error has to stop the command. Running it anyway would turn a sandbox
// that could not be applied into a silent grant of the full permissions.
func TestBashSandboxErrorStopsCommand(t *testing.T) {
	impl := newTool(t, Options{Cwd: t.TempDir(), Sandbox: &fakeSandbox{err: errors.New("sandbox: unavailable")}})

	err := runError(t, impl, `{"command":"echo should-not-run"}`)
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v, want the sandbox's refusal", err)
	}
}

func numberPtr(value int) *tool.Number {
	number := tool.Number(value)
	return &number
}

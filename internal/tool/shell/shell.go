// Package shell implements the bash tool.
//
// pi runs commands through Node's child_process and cleans up with a process
// tree kill. Go has no such API, so the command runs in its own process group
// and a timeout or abort signals the whole group. That is what makes
// "npm test" or a build script actually stop: signalling only the shell leaves
// the grandchildren running and holding the output pipe open.
package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const bashDescription = "Execute a bash command in the current working directory. Returns stdout and stderr. " +
	"Output is truncated to last 2000 lines or 50KB (whichever is hit first). " +
	"If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds."

// bashParameters is the schema, with a slot for the background parameter.
//
// The slot is how a capability that does not exist stays invisible: on a host
// with no background runner the schema is byte for byte what it was before
// background commands existed, so the model is never shown an argument it can
// only fail to use.
const bashParameters = `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to execute"},
    "timeout": {"type": "number", "description": "Timeout in seconds (optional, no default timeout)"}%s
  },
  "required": ["command"]
}`

// backgroundParameter is the text that fills the slot.
const backgroundParameter = `,
    "run_in_background": {"type": "boolean", "description": "Start the command detached and return as soon as it is running. Use this for servers, watches and long builds; read the output with task_output and end it with task_stop."}`

// backgroundUsage is appended to the description only when the parameter exists,
// because a description that names an argument the schema does not carry is
// worse than no description at all: the model would try to use it.
const backgroundUsage = "With run_in_background the command is detached from this call: it keeps running after the result returns, " +
	"its output goes to a file instead of the result, and task_output reads that file. " +
	"Unset, the call waits for the command as it always has."

const (
	// maxTimeoutMs caps the accepted timeout. The bound exists because the value
	// has to fit a timer on every platform pi supports.
	maxTimeoutMs = 2_147_483_647
	// structuredOutputMaxBytes bounds structuredContent.output. The model-facing
	// text is already truncated; this is the budget for programmatic callers.
	structuredOutputMaxBytes = 1024 * 1024
	// updateThrottleMs rate-limits streaming updates. pi uses a timer; a
	// timestamp check is enough here because the reader goroutine is the only
	// producer, and the final state is always emitted at the end.
	updateThrottleMs = 100
)

// Sandbox confines the process tree a command starts.
//
// The interface is declared here rather than in the package that implements it so
// that this package does not depend on internal/sandbox: the tool decides when a
// command is confined, and the caller decides how. A nil Sandbox runs the command
// with the process's own permissions, which is what every build before this one
// did.
type Sandbox interface {
	// Wrap returns the argv and the environment to run in place of the given
	// ones. An error means the command must not run at all: falling back to the
	// unwrapped argv would turn a sandbox that could not be applied into a
	// silent grant of the full process permissions.
	Wrap(argv []string, env []string) ([]string, []string, error)
}

// Options configures the shell tool.
type Options struct {
	// Cwd is the directory commands run in.
	Cwd string
	// ShellPath overrides the detected shell.
	ShellPath string
	// CommandPrefix is prepended to every command, which is how a caller injects
	// shell setup such as environment activation.
	CommandPrefix string
	// Env holds extra environment variables layered over the process
	// environment. The app layer uses it for the PI_* session variables once
	// sessions exist.
	Env map[string]string
	// TempFilePrefix names the file that holds truncated output.
	TempFilePrefix string
	// Spill decides which directory that file goes to. Nil keeps the OS temp
	// directory, which is where it went before the session had a say.
	Spill *tool.Spill
	// Sandbox, when set, confines every command this tool runs.
	Sandbox Sandbox
	// Background, when set, records commands that outlive the call that
	// started them. Nil removes the run_in_background argument from the
	// schema, which is what a host without a task registry should serve: the
	// alternative is offering to start something nothing can list.
	Background tool.Recorder
}

// Tools returns the shell tools. bash is the only one for now; powershell
// belongs in the same package when a Windows-only variant is added.
func Tools(options Options) []tool.Tool {
	return []tool.Tool{&bashTool{options: options}}
}

type bashTool struct{ options Options }

func (t *bashTool) Name() string  { return "bash" }
func (t *bashTool) Label() string { return "bash" }

func (t *bashTool) Description() string {
	if t.options.Background == nil {
		return bashDescription
	}
	return bashDescription + "\n\n" + backgroundUsage
}

func (t *bashTool) Parameters() json.RawMessage {
	extra := ""
	if t.options.Background != nil {
		extra = backgroundParameter
	}
	return json.RawMessage(fmt.Sprintf(bashParameters, extra))
}

type bashRequest struct {
	Command string `json:"command"`
	// Timeout is whole seconds. tool.Number also accepts the string form some
	// models emit; a fractional timeout is rounded down to zero and rejected,
	// which is recorded as a gap in go/docs/architecture.md.
	Timeout *tool.Number `json:"timeout"`
	// RunInBackground detaches the command. A host without a recorder rejects
	// it rather than running it in the foreground, because a model that asked
	// for a detached server and got a blocking call has been told the wrong
	// thing about what happened.
	RunInBackground bool `json:"run_in_background"`
}

// bashDetails is the details payload of a truncated result.
type bashDetails struct {
	Truncation     *tool.Truncation `json:"truncation,omitempty"`
	FullOutputPath string           `json:"fullOutputPath,omitempty"`
}

// backgroundSpec is one command to start detached.
type backgroundSpec struct {
	// recorded is the text the record holds: the model's own command, without
	// the caller's prefix, because the prefix is setup and the command is what
	// the record is about.
	recorded string
	argv     []string
	cwd      string
	// timeout of zero means the background default, which differs from the
	// foreground default on purpose: a detached command with no bound at all
	// is a process the next session inherits and cannot explain.
	timeout time.Duration
}

// backgroundDetails is the details payload of a started background command.
type backgroundDetails struct {
	TaskID     string `json:"taskId"`
	OutputPath string `json:"outputPath,omitempty"`
}

// bashStructuredContent is the structuredContent shape. Field order matches pi
// because the JSON is written into sessions.
type bashStructuredContent struct {
	Output          string  `json:"output"`
	Truncated       bool    `json:"truncated"`
	FullOutputPath  string  `json:"full_output_path,omitempty"`
	ExitCode        int     `json:"exit_code"`
	WallTimeSeconds float64 `json:"wall_time_seconds"`
}

func (t *bashTool) Execute(ctx context.Context, call tool.Call, onUpdate func(tool.Partial)) (tool.Result, error) {
	var request bashRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(request.Command) == "" {
		return tool.Result{}, errors.New("bash: command is required")
	}
	timeout, err := resolveTimeout(request.Timeout)
	if err != nil {
		return tool.Result{}, err
	}

	shellPath := t.options.ShellPath
	if shellPath == "" {
		shellPath = resolveShell()
	}
	if shellPath == "" {
		return tool.Result{}, fmt.Errorf("bash: %s", shellMissingHint)
	}

	cwd := t.options.Cwd
	if cwd == "" {
		cwd = "."
	}
	if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
		return tool.Result{}, fmt.Errorf("Working directory does not exist: %s\nCannot execute bash commands.", cwd)
	}

	command := request.Command
	if t.options.CommandPrefix != "" {
		command = t.options.CommandPrefix + "\n" + command
	}

	if request.RunInBackground {
		return t.background(call, backgroundSpec{
			recorded: request.Command,
			argv:     []string{shellPath, "-c", command},
			cwd:      cwd,
			timeout:  timeout,
		})
	}

	output := newAccumulator(accumulatorOptions{
		maxLines:       tool.DefaultMaxLines,
		maxBytes:       tool.DefaultMaxBytes,
		tempFilePrefix: t.options.TempFilePrefix,
		spill:          t.options.Spill,
	})
	if onUpdate != nil {
		onUpdate(tool.Partial{})
	}

	lastUpdate := time.Now()
	dirty := false
	update := func() {
		if onUpdate == nil || !dirty {
			return
		}
		if time.Since(lastUpdate) < updateThrottleMs*time.Millisecond {
			return
		}
		dirty = false
		lastUpdate = time.Now()
		snapshot := output.snapshot(true)
		onUpdate(tool.Partial{
			Content: []wire.Content{wire.TextContent(snapshot.content)},
			Details: detailsJSON(snapshot),
		})
	}

	startedAt := time.Now()
	outcome, err := runProcess(ctx, processSpec{
		argv:    []string{shellPath, "-c", command},
		cwd:     cwd,
		env:     t.environment(),
		sandbox: t.options.Sandbox,
		timeout: timeout,
		onData: func(data []byte) {
			output.append(data)
			dirty = true
			update()
		},
	})
	if err != nil {
		return tool.Result{}, err
	}

	output.finish()
	dirty = true
	update()
	snapshot := output.snapshot(true)
	if err := output.closeTempFile(); err != nil {
		return tool.Result{}, err
	}

	// A killed or timed-out command reports its partial output, because that
	// output is usually what explains the failure. These come back as an error
	// result rather than a Go error so the text survives the agent's abort
	// handling.
	switch {
	case outcome.aborted:
		return errorResult(appendStatus(formatText(output, snapshot, ""), "Command aborted"), detailsJSON(snapshot)), nil
	case outcome.timedOut:
		seconds := strconvFloat(request.Timeout)
		return errorResult(appendStatus(formatText(output, snapshot, ""),
			fmt.Sprintf("Command timed out after %s seconds", seconds)), detailsJSON(snapshot)), nil
	}

	text := formatText(output, snapshot, "(no output)")
	wallTimeSeconds := math.Round(float64(time.Since(startedAt).Milliseconds())/100) / 10

	full, err := output.readFullOutput(structuredOutputMaxBytes)
	if err != nil {
		return tool.Result{}, err
	}
	structured := bashStructuredContent{
		Output:          full.content,
		Truncated:       full.truncated,
		ExitCode:        outcome.exitCode,
		WallTimeSeconds: wallTimeSeconds,
	}
	if full.truncated {
		structured.FullOutputPath = snapshot.fullOutputPath
	}
	structuredJSON, err := json.Marshal(structured)
	if err != nil {
		return tool.Result{}, err
	}

	if outcome.exitCode != 0 {
		return tool.Result{
			Content:           []wire.Content{wire.TextContent(appendStatus(text, fmt.Sprintf("Command exited with code %d", outcome.exitCode)))},
			Details:           detailsJSON(snapshot),
			StructuredContent: structuredJSON,
			IsError:           true,
		}, nil
	}

	return tool.Result{
		Content:           []wire.Content{wire.TextContent(text)},
		Details:           detailsJSON(snapshot),
		StructuredContent: structuredJSON,
	}, nil
}

// environment layers the configured variables over the process environment,
// keeping the order stable so a test can assert on it.
func (t *bashTool) environment() []string {
	base := os.Environ()
	if len(t.options.Env) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(t.options.Env))
	order := make([]string, 0, len(base)+len(t.options.Env))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, seen := merged[key]; !seen {
			order = append(order, key)
		}
		merged[key] = value
	}
	for key, value := range t.options.Env {
		if _, seen := merged[key]; !seen {
			order = append(order, key)
		}
		merged[key] = value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+merged[key])
	}
	return out
}

// argvOf applies the sandbox, if one is configured, and returns the program to
// run and the environment to run it with.
//
// A wrap error is returned rather than ignored: the unwrapped command would run
// with the process's full permissions, so a sandbox that could not be applied
// would become a silent grant. The two platform files call this so that the
// decision is made once, and so neither of them has to know a sandbox exists.
func (spec processSpec) argvOf() ([]string, []string, error) {
	if spec.sandbox == nil {
		return spec.argv, spec.env, nil
	}
	argv, env, err := spec.sandbox.Wrap(spec.argv, spec.env)
	if err != nil {
		return nil, nil, err
	}
	if len(argv) == 0 {
		return nil, nil, errors.New("bash: the sandbox returned an empty command")
	}
	return argv, env, nil
}

// resolveTimeout converts the seconds argument to a duration. A zero duration
// means no timeout, which is pi's default: a command runs until it finishes or
// the caller cancels.
func resolveTimeout(timeout *tool.Number) (time.Duration, error) {
	if timeout == nil {
		return 0, nil
	}
	seconds := timeout.Int()
	if seconds <= 0 {
		return 0, errors.New("Invalid timeout: must be a finite number of seconds")
	}
	ms := int64(seconds) * 1000
	if ms > maxTimeoutMs {
		return 0, fmt.Errorf("Invalid timeout: maximum is %s seconds",
			strconv.FormatFloat(float64(maxTimeoutMs)/1000, 'f', -1, 64))
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// formatText renders the model-facing output and appends the notice that says
// where the untruncated text went.
func formatText(output *accumulator, snapshot outputSnapshot, emptyText string) string {
	text := snapshot.content
	if text == "" {
		text = emptyText
	}
	truncation := snapshot.truncation
	if !truncation.Truncated {
		return text
	}
	startLine := truncation.TotalLines - truncation.OutputLines + 1
	endLine := truncation.TotalLines
	switch {
	case truncation.LastLinePartial:
		text += fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s). Full output: %s]",
			tool.FormatSize(truncation.OutputBytes), endLine,
			tool.FormatSize(output.getLastLineBytes()), snapshot.fullOutputPath)
	case truncation.TruncatedBy == "lines":
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]",
			startLine, endLine, truncation.TotalLines, snapshot.fullOutputPath)
	default:
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]",
			startLine, endLine, truncation.TotalLines,
			tool.FormatSize(tool.DefaultMaxBytes), snapshot.fullOutputPath)
	}
	return text
}

// detailsJSON reports the truncation and the path, and is nil when the output
// fit.
func detailsJSON(snapshot outputSnapshot) json.RawMessage {
	if !snapshot.truncation.Truncated {
		return nil
	}
	truncation := snapshot.truncation
	payload, err := json.Marshal(bashDetails{Truncation: &truncation, FullOutputPath: snapshot.fullOutputPath})
	if err != nil {
		return nil
	}
	return payload
}

func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}

func errorResult(text string, details json.RawMessage) tool.Result {
	if details == nil {
		details = json.RawMessage("{}")
	}
	return tool.Result{
		Content: []wire.Content{wire.TextContent(text)},
		Details: details,
		IsError: true,
	}
}

// strconvFloat renders a seconds value the way the model wrote it, without a
// trailing ".0" or an exponent.
func strconvFloat(timeout *tool.Number) string {
	if timeout == nil {
		return "0"
	}
	return strconv.FormatFloat(float64(timeout.Int()), 'f', -1, 64)
}

// Package tasks implements task_query, task_output and task_stop.
//
// The three tools are separate because they answer three different questions —
// what is running, what did it say, and stop it — and a single tool with a mode
// field would have to declare the union of their arguments. That is the same
// reason git_status and git_diff are separate.
//
// None of them produces work: a task exists because bash started it with
// run_in_background. That is why this package is small, and why a host without a
// background runner registers none of these tools rather than three that always
// answer "nothing".
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pi-go/internal/task"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// outputChunkBytes bounds one task_output result.
//
// A background command can print megabytes, and a reader that returns all of it
// spends the context budget of the turn it was called in. 32KB is several
// screens of build log, and the offset in the reply makes reading the rest one
// more call rather than a failed one.
const outputChunkBytes = 32 * 1024

// maxWaitMs bounds how long task_output blocks waiting for new output.
//
// The bound exists because a tool call holds the turn: a wait that ran as long as
// the caller asked would look like a hung session. 30 seconds is long enough to
// cover the gap between two lines of a build and short enough to be
// interruptible by the user without wondering what is happening.
const maxWaitMs = 30_000

// pollIntervalMs is how often a waiting read checks the file.
const pollIntervalMs = 100

// Tools returns the three task tools.
func Tools(service *task.Service) []tool.Tool {
	return []tool.Tool{
		&queryTool{service: service},
		&outputTool{service: service},
		&stopTool{service: service},
	}
}

const queryParameters = `{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "One task id. Omit to list every task of this session."},
    "status": {"type": "string", "description": "Keep only tasks in this state: queued, running, stopping, succeeded, failed, canceled or lost. \"active\" and \"finished\" are shortcuts for the two groups."},
    "all_sessions": {"type": "boolean", "description": "Include tasks started by other conversations in this directory. Default false: the answer is what this session started."}
  }
}`

const queryDescription = "List background commands, or show one in full. " +
	"A task is a command started by bash with run_in_background; it keeps running after that call returned. " +
	"Use this to find the id task_output and task_stop need. " +
	"Read-only."

const outputParameters = `{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Task id, as returned by bash or task_query."},
    "offset": {"type": "number", "description": "Byte offset to continue from. Omit to read from the beginning; pass the next offset printed in the previous reply to read only what is new."},
    "wait_ms": {"type": "number", "description": "Wait up to this many milliseconds for output to arrive, instead of returning what is there now. Maximum 30000."}
  },
  "required": ["id"]
}`

const outputDescription = "Read the output of a background command. " +
	"Both streams are in one file, interleaved in the order they were written. " +
	"Each reply ends with the task's state and the offset to continue from, so a long-running command is read by calling this again with that offset rather than from the start. " +
	"Read-only; it never stops the command."

const stopParameters = `{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Task id, as returned by bash or task_query."}
  },
  "required": ["id"]
}`

const stopDescription = "Stop a background command by signalling its process group, so everything it started ends with it, not only the shell. " +
	"Stopping is terminal: the task records canceled and its output stays readable. " +
	"A task that already finished is reported, not restarted or killed again."

type queryTool struct{ service *task.Service }

func (t *queryTool) Name() string                { return "task_query" }
func (t *queryTool) Label() string               { return "task_query" }
func (t *queryTool) Description() string         { return queryDescription }
func (t *queryTool) Parameters() json.RawMessage { return json.RawMessage(queryParameters) }

type queryRequest struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	AllSessions bool   `json:"all_sessions"`
}

func (t *queryTool) Execute(_ context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var request queryRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	statuses, err := statusesOf(request.Status)
	if err != nil {
		return tool.Result{}, err
	}
	// One id is one answer, so the filter narrows a listing and not a lookup:
	// filtering a named task by status could only ever produce "not found" for
	// a task the caller is holding the id of.
	filter := task.Filter{ID: request.ID, AllSessions: request.AllSessions}
	if request.ID == "" {
		filter.Statuses = statuses
	}
	found, err := t.service.Query(filter)
	if err != nil {
		return tool.Result{}, err
	}
	if request.ID != "" {
		if len(found) == 0 {
			return tool.Result{}, fmt.Errorf("%w: %s", task.ErrNotFound, request.ID)
		}
		return textResult(describe(found[0], t.service.Now())), nil
	}
	return textResult(listing(found, t.service.Now())), nil
}

type outputTool struct{ service *task.Service }

func (t *outputTool) Name() string                { return "task_output" }
func (t *outputTool) Label() string               { return "task_output" }
func (t *outputTool) Description() string         { return outputDescription }
func (t *outputTool) Parameters() json.RawMessage { return json.RawMessage(outputParameters) }

type outputRequest struct {
	ID     string       `json:"id"`
	Offset *tool.Number `json:"offset"`
	WaitMs *tool.Number `json:"wait_ms"`
}

func (t *outputTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var request outputRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(request.ID) == "" {
		return tool.Result{}, errors.New("task_output: id is required")
	}
	var offset int64
	if request.Offset != nil {
		value := request.Offset.Int()
		if value < 0 {
			return tool.Result{}, errors.New("task_output: offset must not be negative")
		}
		offset = int64(value)
	}
	var wait time.Duration
	if request.WaitMs != nil {
		ms := request.WaitMs.Int()
		if ms < 0 {
			return tool.Result{}, errors.New("task_output: wait_ms must not be negative")
		}
		if ms > maxWaitMs {
			ms = maxWaitMs
		}
		wait = time.Duration(ms) * time.Millisecond
	}

	read, err := t.await(ctx, request.ID, offset, wait)
	if err != nil {
		return tool.Result{}, err
	}
	record, err := t.service.Get(request.ID)
	if err != nil {
		return tool.Result{}, err
	}
	body := read.Content
	if body == "" {
		if read.Done {
			body = "(no output)"
		} else {
			body = "(no output yet)"
		}
	}
	return textResult(body + "\n\n" + summary(record, read)), nil
}

// await reads once, and again every pollIntervalMs while the caller asked to
// wait and nothing has arrived yet.
//
// Waiting is done here rather than in the service because it is a property of
// the question, not of the record: task_query must never block, and task_output
// must block only when it was told to.
func (t *outputTool) await(ctx context.Context, id string, offset int64, wait time.Duration) (task.ReadResult, error) {
	deadline := time.Now().Add(wait)
	for {
		read, err := t.service.Read(id, offset, outputChunkBytes)
		if err != nil {
			return task.ReadResult{}, err
		}
		if read.Content != "" || read.Done || wait <= 0 || !time.Now().Before(deadline) {
			return read, nil
		}
		select {
		case <-ctx.Done():
			return read, nil
		case <-time.After(pollIntervalMs * time.Millisecond):
		}
	}
}

type stopTool struct{ service *task.Service }

func (t *stopTool) Name() string                { return "task_stop" }
func (t *stopTool) Label() string               { return "task_stop" }
func (t *stopTool) Description() string         { return stopDescription }
func (t *stopTool) Parameters() json.RawMessage { return json.RawMessage(stopParameters) }

type stopRequest struct {
	ID string `json:"id"`
}

func (t *stopTool) Execute(_ context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var request stopRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(request.ID) == "" {
		return tool.Result{}, errors.New("task_stop: id is required")
	}
	if err := t.service.Stop(request.ID); err != nil {
		// A task that already ended is not a failed call: the caller wanted it
		// stopped and it is stopped. Reporting that as an error would make the
		// model retry a stop that cannot do anything else.
		if errors.Is(err, task.ErrFinished) {
			record, getErr := t.service.Get(request.ID)
			if getErr != nil {
				return tool.Result{}, getErr
			}
			return textResult(fmt.Sprintf("Nothing to stop: %s is %s.\n\n%s", record.ID, record.Status, describe(record, t.service.Now()))), nil
		}
		return tool.Result{}, err
	}
	record, err := t.service.Get(request.ID)
	if err != nil {
		return tool.Result{}, err
	}
	return textResult("Stopped.\n\n" + describe(record, t.service.Now())), nil
}

// listing renders the tasks as one line each.
func listing(found []task.Task, now time.Time) string {
	if len(found) == 0 {
		return "No tasks. bash with run_in_background is what starts one."
	}
	lines := make([]string, 0, len(found)+1)
	for _, record := range found {
		line := fmt.Sprintf("%s  %-9s  %6s  %s", record.ID, record.Status, age(record, now), descriptionOf(record))
		if record.ExitCode != nil {
			line += fmt.Sprintf("  (exit %d)", *record.ExitCode)
		}
		lines = append(lines, line)
	}
	active := 0
	for _, record := range found {
		if record.Status.Active() {
			active++
		}
	}
	lines = append(lines, "", fmt.Sprintf("%d task(s), %d active.", len(found), active))
	return strings.Join(lines, "\n")
}

// describe renders one task in full, which is what a caller wants before reading
// output or stopping it: where it ran, how long ago, and why it ended.
func describe(record task.Task, now time.Time) string {
	lines := []string{
		fmt.Sprintf("%s  %s  %s", record.ID, record.Status, age(record, now)),
		fmt.Sprintf("Kind: %s", record.Kind),
	}
	if record.Description != "" {
		lines = append(lines, fmt.Sprintf("What: %s", record.Description))
	}
	if record.Command != "" {
		lines = append(lines, "Command:")
		for _, line := range strings.Split(strings.TrimRight(record.Command, "\n"), "\n") {
			lines = append(lines, "  "+line)
		}
	}
	if record.Cwd != "" {
		lines = append(lines, fmt.Sprintf("Cwd: %s", record.Cwd))
	}
	if record.OutputPath != "" {
		lines = append(lines, fmt.Sprintf("Output: %s", record.OutputPath))
	}
	if record.ExitCode != nil {
		lines = append(lines, fmt.Sprintf("Exit: %d", *record.ExitCode))
	}
	if record.Error != "" {
		lines = append(lines, fmt.Sprintf("Error: %s", record.Error))
	}
	return strings.Join(lines, "\n")
}

// summary is the trailer task_output always ends with.
//
// It is always present rather than only on truncation: a reader that has to
// infer whether more output can arrive will read until the file stops growing,
// and a reader that is told cannot.
func summary(record task.Task, read task.ReadResult) string {
	state := record.Status.String()
	if read.Done {
		return fmt.Sprintf("[task %s %s, %d bytes, complete]", record.ID, state, read.TotalBytes)
	}
	return fmt.Sprintf("[task %s %s, %d bytes total, next offset %d]", record.ID, state, read.TotalBytes, read.NextOffset)
}

func textResult(text string) tool.Result {
	return tool.Result{Content: []wire.Content{wire.TextContent(text)}}
}

func descriptionOf(record task.Task) string {
	if record.Description != "" {
		return record.Description
	}
	if record.Command != "" {
		line, _, _ := strings.Cut(record.Command, "\n")
		return line
	}
	return "(no command)"
}

func age(record task.Task, now time.Time) string {
	return record.Age(now).Round(time.Second).String()
}

// statusesOf turns the status argument into the filter the store understands.
//
// The two groups are accepted because they are what a caller actually means: a
// model asking "what is running" does not want to spell out queued and
// stopping, and one asking what finished does not want to spell out four
// terminal states and then forget one.
func statusesOf(name string) ([]task.Status, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "all":
		return nil, nil
	case "active":
		return []task.Status{task.StatusQueued, task.StatusRunning, task.StatusStopping}, nil
	case "finished", "terminal":
		return []task.Status{task.StatusSucceeded, task.StatusFailed, task.StatusCanceled, task.StatusLost}, nil
	}
	status := task.Status(strings.ToLower(strings.TrimSpace(name)))
	known := []task.Status{
		task.StatusQueued, task.StatusRunning, task.StatusStopping,
		task.StatusSucceeded, task.StatusFailed, task.StatusCanceled, task.StatusLost,
	}
	for _, candidate := range known {
		if candidate == status {
			return []task.Status{status}, nil
		}
	}
	return nil, fmt.Errorf("task_query: unknown status %q", name)
}

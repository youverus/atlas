// Package task records commands that outlive the tool call that started them.
//
// A task is a row, not a goroutine. The process belongs to whoever started it
// (the shell tool, which already knows how to run one in its own group), and
// this package only keeps the record: what was run, by which session, since
// when, into which file, and how it ended. Splitting it that way is what makes
// the record readable by a call that knows nothing about the process, and the
// process killable by a call that knows nothing about the record.
//
// A task's lifetime is the process's lifetime. There is no daemon here, so a
// task that outlived pi would be a process nobody can list or stop; Close stops
// everything this process started, and a row that is still open when the
// process is gone reads as lost (see Service.Query). Surviving a restart is
// what a runtime daemon buys, and this package deliberately does not pretend to
// have one.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Kind is what a task runs.
//
// Only bash exists today. The vocabulary is declared whole because the record
// is written to disk and read back by a later build: a kind this file has never
// heard of has to be describable rather than refused, and the three missing
// ones are the shapes the background work of a runtime takes.
type Kind string

const (
	// KindBash is a shell command.
	KindBash Kind = "bash"
	// KindSubagent is a child session driven by the model. Not implemented:
	// go/docs/architecture.md section 9.3 lists it as future work.
	KindSubagent Kind = "subagent"
	// KindWorkflow is a named sequence of steps.
	KindWorkflow Kind = "workflow"
	// KindCustom is anything a host registers itself.
	KindCustom Kind = "custom"
)

// Status is where a task is in its life.
//
// queued exists for the instant between the record being created and the
// process being started: a row that says running before anything runs would lie
// about a start that can still fail.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusStopping  Status = "stopping"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusLost      Status = "lost"
)

// Terminal reports whether nothing more will happen to a task in this status.
//
// Terminal is final: Settled refuses to move a task out of one, because the
// only ways to get here are a process that exited, a stop that was asked for,
// and a process that is gone. A late report from a killed group racing a
// stop must not turn "canceled" back into "succeeded".
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCanceled, StatusLost:
		return true
	default:
		return false
	}
}

// Active reports whether a task is still doing something.
func (s Status) Active() bool {
	return s == StatusQueued || s == StatusRunning || s == StatusStopping
}

// String is the status as it is written in the file and printed in a listing.
func (s Status) String() string { return string(s) }

// canTransition is the status machine, written as a table rather than as
// scattered checks: a record on disk is read back by code that did not write
// it, so "which moves are legal" has to have one answer.
func canTransition(from, to Status) bool {
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	switch from {
	case StatusQueued:
		return to == StatusRunning || to.Terminal()
	case StatusRunning:
		return to == StatusStopping || to.Terminal()
	case StatusStopping:
		return to.Terminal()
	default:
		return false
	}
}

// Task is one background command.
//
// The JSON tags are the file format. They are spelled out rather than derived
// from the field names because these records are read by a build that may have
// renamed a field, and a silent rename would make every old row unreadable.
type Task struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Status Status `json:"status"`
	// OwnerSessionID is the conversation that started it. It is what task_query
	// filters by: a task belongs to the conversation that asked for it, not to
	// the process and not to the directory.
	OwnerSessionID string `json:"ownerSessionId"`
	// OwnerPID is the process that ran it. It is the only thing that can tell a
	// live task from one whose process died, so it is written even though it
	// makes the record machine-specific.
	OwnerPID int `json:"ownerPid"`
	// PGID is the process group, which is what Stop signals. It is recorded
	// rather than looked up because the process may be gone by the time the
	// caller wants to stop it, and the group id is all a kill needs.
	PGID int `json:"pgid,omitempty"`
	// Command is what was run, verbatim. The model asked for it, so it is the
	// model's own text and is never parsed here.
	Command string `json:"command,omitempty"`
	// Description is a one-line summary for listings, which is what a task list
	// shows instead of a 400-character command.
	Description string `json:"description,omitempty"`
	// Cwd is where it ran. A resumed session can belong to another directory,
	// and a record that does not say where it ran describes nothing.
	Cwd string `json:"cwd,omitempty"`
	// ToolCallID is the call that started it, so a transcript can pair the row
	// with the message that made it.
	ToolCallID string `json:"toolCallId,omitempty"`
	// OutputPath is the file holding stdout and stderr interleaved. Nil means
	// the output went nowhere, which is what a command that never started
	// leaves behind.
	OutputPath string `json:"outputPath,omitempty"`
	// ExitCode is nil until the process exits.
	ExitCode *int   `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`

	CreatedAtMs int64 `json:"createdAtMs"`
	StartedAtMs int64 `json:"startedAtMs,omitempty"`
	EndedAtMs   int64 `json:"endedAtMs,omitempty"`
}

// Age is how long the task has been doing what it is doing, measured against
// now rather than against the end time when it has one.
func (t Task) Age(now time.Time) time.Duration {
	end := now
	if t.EndedAtMs > 0 {
		end = time.UnixMilli(t.EndedAtMs)
	}
	start := t.CreatedAtMs
	if t.StartedAtMs > 0 {
		start = t.StartedAtMs
	}
	if end.Before(time.UnixMilli(start)) {
		return 0
	}
	return end.Sub(time.UnixMilli(start))
}

// newID returns a task id. It is short because it is typed by a reader and
// printed in a listing: 48 bits of randomness is enough to keep ids distinct
// within one project's task file, which is the only place they have to be
// unique.
func newID(taken map[string]bool) string {
	for i := 0; i < 100; i++ {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			break
		}
		id := "bg_" + hex.EncodeToString(b[:])
		if !taken[id] {
			return id
		}
	}
	// Randomness that cannot produce a fresh id is not recoverable, and a panic
	// in the middle of a turn is worse than a timestamp-shaped id.
	return fmt.Sprintf("bg_%d", time.Now().UnixNano())
}

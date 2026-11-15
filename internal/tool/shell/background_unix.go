//go:build unix

package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// backgroundTimeLimit bounds a detached command that did not ask for a timeout.
//
// The foreground default is no timeout, because a caller is waiting and can
// interrupt. A detached command has no caller left, so the choice is between a
// bound and a process that runs until the machine restarts. One hour is the same
// bound the runtime this mirrors puts on background work.
const backgroundTimeLimit = time.Hour

// background starts a command detached from the tool call that asked for it.
//
// It never waits. The row is written, the process is started in its own group,
// and a goroutine owns the wait. That goroutine is also why output goes to a
// file instead of a pipe: a pipe needs a reader, and a reader that stops when
// the call returns is a command that blocks on a full buffer the moment its
// output outgrows it.
func (t *bashTool) background(call tool.Call, spec backgroundSpec) (tool.Result, error) {
	recorder := t.options.Background
	if recorder == nil {
		return tool.Result{}, errors.New("bash: background commands are not available here")
	}
	argv, env, err := processSpec{argv: spec.argv, env: t.environment(), sandbox: t.options.Sandbox}.argvOf()
	if err != nil {
		return tool.Result{}, err
	}

	// The row exists before the process does. A crash between the two leaves a
	// queued row, which reads as "started and never ran" rather than as a
	// command that appeared from nowhere and wrote to a file nobody named.
	id, output, err := recorder.Register(tool.Background{
		Command:     spec.recorded,
		Description: describeCommand(spec.recorded),
		Cwd:         spec.cwd,
		ToolCallID:  call.ID,
	})
	if err != nil {
		return tool.Result{}, err
	}

	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = spec.cwd
	command.Env = env
	// Its own group is what makes the command stoppable later: a stop signals
	// this pid negatively, and signalling only the shell would leave a server's
	// children holding the port.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Stdin stays nil, which exec turns into /dev/null. A detached command
	// cannot ask a question, so it must not wait for an answer either.
	command.Stdin = nil
	command.Stdout = output
	command.Stderr = output

	if err := command.Start(); err != nil {
		if output != nil {
			_ = output.Close()
		}
		recorder.Settled(id, tool.BackgroundExit{Err: err, EndedAt: time.Now()})
		return tool.Result{}, err
	}

	startedAt := time.Now()
	recorder.Started(id, command.Process.Pid)

	timeout := spec.timeout
	if timeout <= 0 {
		timeout = backgroundTimeLimit
	}
	go waitDetached(command, output, startedAt, timeout, func(exit tool.BackgroundExit) {
		recorder.Settled(id, exit)
	})

	return backgroundResult(id, output, timeout)
}

// waitDetached owns the end of a detached run.
//
// The timeout is an AfterFunc rather than a select because there is nothing to
// select on: nobody is waiting for this process, so the only interesting moments
// are its exit and the deadline.
func waitDetached(command *exec.Cmd, output *os.File, startedAt time.Time, timeout time.Duration, settle func(tool.BackgroundExit)) {
	var killed atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		killed.Store(true)
		killGroup(command.Process.Pid)
	})
	waitErr := command.Wait()
	// Stopping a timer that already fired is a no-op, and one that has not
	// fired keeps a reference to a finished command until it does.
	timer.Stop()
	if output != nil {
		// The child held its own descriptor; the parent's copy is only in the
		// way once nothing else writes through it.
		_ = output.Close()
	}

	exit := tool.BackgroundExit{
		Code:      exitCodeOf(command),
		TimedOut:  killed.Load(),
		StartedAt: startedAt,
		EndedAt:   time.Now(),
	}
	// A process that never produced a state did not run at all, so the wait
	// error is the only description of what happened.
	if waitErr != nil && command.ProcessState == nil {
		exit.Err = waitErr
	}
	settle(exit)
}

// backgroundResult is the text the model sees when a command was started.
//
// It names the id twice on purpose: the model has to carry it into task_output
// and into task_stop, and a result that mentions it once as prose is a result
// that gets paraphrased.
func backgroundResult(id string, output *os.File, timeout time.Duration) (tool.Result, error) {
	lines := []string{
		fmt.Sprintf("Started background command %s.", id),
	}
	if output != nil {
		lines = append(lines, fmt.Sprintf("Output: %s", output.Name()))
	} else {
		lines = append(lines, "Output is not being saved: there is no writable directory for it.")
	}
	lines = append(lines,
		fmt.Sprintf("It keeps running after this call returns, and stops itself after %s.", durationText(timeout)),
		fmt.Sprintf("Read it with task_output (id %q) and end it with task_stop (id %q).", id, id),
	)

	details, err := json.Marshal(backgroundDetails{TaskID: id, OutputPath: outputName(output)})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		Content: []wire.Content{wire.TextContent(strings.Join(lines, "\n"))},
		Details: details,
	}, nil
}

// describeCommand is the one-line summary a listing shows instead of the whole
// command. Multi-line commands collapse to their first line, because a listing
// row that wraps is a listing nobody scans.
func describeCommand(command string) string {
	line, _, _ := strings.Cut(command, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "(empty command)"
	}
	const limit = 80
	if len(line) > limit {
		// Byte slicing can split a rune, so the cut is walked back to a rune
		// boundary.
		cut := limit
		for cut > 0 && !utf8Start(line[cut]) {
			cut--
		}
		return line[:cut] + "..."
	}
	return line
}

// utf8Start reports whether b begins a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xc0 != 0x80 }

// durationText renders a timeout the way a sentence needs it rather than the way
// time.Duration prints it: "1 hour", not "1h0m0s".
func durationText(d time.Duration) string {
	unit, count := "second", int(d/time.Second)
	switch {
	case d%time.Hour == 0:
		unit, count = "hour", int(d/time.Hour)
	case d%time.Minute == 0:
		unit, count = "minute", int(d/time.Minute)
	}
	if count != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", count, unit)
}

func outputName(output *os.File) string {
	if output == nil {
		return ""
	}
	return output.Name()
}

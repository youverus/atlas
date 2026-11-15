//go:build unix

package shell

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// processSpec is one command to run.
type processSpec struct {
	argv []string
	cwd  string
	env  []string
	// sandbox, when set, rewrites argv and env so the command runs confined.
	sandbox Sandbox
	// timeout of zero means no timeout, matching pi: a command may run for as
	// long as the caller allows.
	timeout time.Duration
	onData  func([]byte)
}

type processResult struct {
	exitCode int
	aborted  bool
	timedOut bool
	// signal is the terminating signal number, zero when the process exited
	// normally.
	signal int
}

// runProcess runs the command in its own process group so a timeout or an abort
// can kill everything the command started, not just the shell. A build step that
// spawns compilers would otherwise survive the kill and keep writing output.
func runProcess(ctx context.Context, spec processSpec) (processResult, error) {
	argv, env, err := spec.argvOf()
	if err != nil {
		return processResult{}, err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = spec.cwd
	command.Env = env
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Stdin stays nil, which exec turns into /dev/null. An interactive prompt
	// would otherwise block until the timeout.
	command.Stdin = nil

	reader, writer, err := os.Pipe()
	if err != nil {
		return processResult{}, err
	}
	// Both streams share one pipe so the combined output keeps the ordering the
	// terminal would have shown.
	command.Stdout = writer
	command.Stderr = writer

	if err := command.Start(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return processResult{}, err
	}
	// The parent must drop its copy of the write end or the reader never sees
	// EOF.
	_ = writer.Close()

	collected := make(chan struct{})
	go func() {
		defer close(collected)
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := reader.Read(buffer)
			if read > 0 && spec.onData != nil {
				spec.onData(buffer[:read])
			}
			if readErr != nil {
				return
			}
		}
	}()

	pgid := command.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var timeout <-chan time.Time
	if spec.timeout > 0 {
		timer := time.NewTimer(spec.timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	result := processResult{}
	select {
	case <-waited:
	case <-timeout:
		result.timedOut = true
		killGroup(pgid)
		<-waited
	case <-ctx.Done():
		result.aborted = true
		killGroup(pgid)
		<-waited
	}

	// Wait for the reader to drain. A background process that inherited the pipe
	// can hold it open past the shell's exit, so the wait is bounded instead of
	// unconditional.
	select {
	case <-collected:
	case <-time.After(500 * time.Millisecond):
	}
	_ = reader.Close()

	result.exitCode = exitCodeOf(command)
	return result, nil
}

// killGroup signals every process in the group. The leader is included because a
// negative pid targets the group.
func killGroup(pgid int) {
	if pgid <= 1 {
		return
	}
	// SIGTERM first so a shell can run its traps, then SIGKILL for anything that
	// ignored it.
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// exitCodeOf maps the process state to a shell exit code, using the 128+signal
// convention so a killed command is not mistaken for success.
func exitCodeOf(command *exec.Cmd) int {
	if command.ProcessState == nil {
		return 1
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return command.ProcessState.ExitCode()
}

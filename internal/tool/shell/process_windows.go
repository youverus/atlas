//go:build windows

package shell

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// processSpec is one command to run.
type processSpec struct {
	argv []string
	cwd  string
	env  []string
	// sandbox, when set, rewrites argv and env so the command runs confined.
	sandbox Sandbox
	timeout time.Duration
	onData  func([]byte)
}

type processResult struct {
	exitCode int
	aborted  bool
	timedOut bool
}

// runProcess runs the command on Windows.
//
// Gap versus unix: there is no process group to signal, so killing relies on
// taskkill walking the tree by parent pid. A grandchild that reparents itself
// can survive. This is recorded in go/docs/architecture.md.
func runProcess(ctx context.Context, spec processSpec) (processResult, error) {
	argv, env, err := spec.argvOf()
	if err != nil {
		return processResult{}, err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = spec.cwd
	command.Env = env
	command.Stdin = nil

	reader, writer, err := os.Pipe()
	if err != nil {
		return processResult{}, err
	}
	command.Stdout = writer
	command.Stderr = writer

	if err := command.Start(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return processResult{}, err
	}
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

	pid := command.Process.Pid
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
		killTree(pid)
		<-waited
	case <-ctx.Done():
		result.aborted = true
		killTree(pid)
		<-waited
	}

	select {
	case <-collected:
	case <-time.After(500 * time.Millisecond):
	}
	_ = reader.Close()

	result.exitCode = exitCodeOf(command)
	return result, nil
}

// killTree asks taskkill to walk the process tree, then falls back to a direct
// kill when taskkill is unavailable.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	taskkill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if err := taskkill.Run(); err == nil {
		return
	}
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}

func exitCodeOf(command *exec.Cmd) int {
	if command.ProcessState == nil {
		return 1
	}
	return command.ProcessState.ExitCode()
}

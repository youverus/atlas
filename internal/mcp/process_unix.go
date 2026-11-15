//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
	"time"
)

// confine puts the child in its own process group so the group can be signalled
// as a whole. Every stdio server in practice is started through a wrapper —
// npx -y, uvx, docker run — and the process that speaks the protocol is the
// wrapper's child.
func confine(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup terminates every process in the group: SIGTERM first so a server can
// flush and clean up, then SIGKILL for anything that ignored it.
//
// The ladder is the one internal/tool/shell uses; the two packages do not share
// it because neither is allowed to depend on the other, and a 20-line signal
// helper is not worth a third package.
func killGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	pid := command.Process.Pid
	if pid <= 1 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

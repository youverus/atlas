//go:build unix

package task

import (
	"errors"
	"syscall"
	"time"
)

// processAlive reports whether pid still exists.
//
// Signal zero performs the permission and existence checks without delivering
// anything. EPERM counts as alive: the process is there, it is simply not ours
// to signal, which is the answer a listing needs.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// signalGroup stops every process in the group.
//
// SIGTERM first, so a shell can run its traps and a build can remove its
// temporary files, then SIGKILL for whatever ignored it. The wait is bounded
// and short: this runs on the tool call that asked to stop, and a caller that
// asked for a stop should not wait a second for a polite request to be
// considered.
func signalGroup(pgid int) {
	if pgid <= 1 {
		return
	}
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

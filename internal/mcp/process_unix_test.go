//go:build unix

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStdioKillsTheProcessGroup is the reason the child gets its own group. The
// fake server starts a grandchild, the way npx and uvx do, and closing the
// connection has to take both down; killing only the direct child leaves the
// process that owns the pipes running for the rest of the day.
func TestStdioKillsTheProcessGroup(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(envChildPID, pidPath)

	set := Open(context.Background(), []Spec{fakeSpec(t, "group")}, Identity{Name: "atlas", Version: "0.7.0"})
	server := set.Servers()[0]
	if server.Err != nil {
		t.Fatalf("connect: %v", server.Err)
	}

	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read the grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid %q: %v", raw, err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("grandchild %d is not running: %v", pid, err)
	}

	set.Close()

	// The group is signalled with a ladder inside Close, and a process that
	// ignored SIGTERM is killed after it. Polling keeps the assertion from
	// depending on which rung did the work.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d is still running after Close", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

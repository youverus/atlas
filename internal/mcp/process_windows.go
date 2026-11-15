//go:build windows

package mcp

import "os/exec"

// confine does nothing on Windows. A process group that can be signalled as a
// unit would need a job object, which this version does not build; see the
// Windows note in docs/mcp.md.
func confine(command *exec.Cmd) {}

// killGroup terminates the direct child only. Under a wrapper (npx, uvx) that
// leaves the real server running; the documented answer is to wrap such a
// server in something that forwards the kill itself.
func killGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}

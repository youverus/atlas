//go:build !unix

package task

// processAlive cannot answer on this platform, so it reports true. Guessing
// "dead" would mark another process's running task as lost, which is a wrong
// claim about the world; guessing "alive" only means the row keeps showing the
// status it was written with.
func processAlive(pid int) bool { return true }

// signalGroup is unavailable: no detached runner exists here either, so no
// group is ever recorded. Stop and Close fail earlier with ErrUnsupported.
func signalGroup(pgid int) {}

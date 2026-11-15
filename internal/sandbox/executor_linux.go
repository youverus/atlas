//go:build linux

package sandbox

import "os/exec"

// defaultExecutor locates bubblewrap on Linux.
//
// Unlike macOS, where /usr/bin/sandbox-exec ships with the base system, bwrap is
// a package the machine may or may not have installed. Missing it is reported
// by name rather than degraded silently: a sandbox block in config.json is a
// statement that unconfined execution is unacceptable.
func defaultExecutor() (string, string) {
	if path, err := exec.LookPath("bwrap"); err == nil {
		return path, ""
	}
	for _, candidate := range []string{"/usr/bin/bwrap", "/bin/bwrap"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, ""
		}
	}
	return "", "bubblewrap (bwrap) not installed"
}

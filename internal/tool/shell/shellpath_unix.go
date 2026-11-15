//go:build unix

package shell

import (
	"os"
	"os/exec"
)

// resolveShell picks the shell to run commands with. pi checks /bin/bash, then
// bash on PATH, then falls back to sh. Go's exec.LookPath replaces the `which`
// call pi makes, so the result does not depend on which tools are installed.
func resolveShell() string {
	if pathExists("/bin/bash") {
		return "/bin/bash"
	}
	if path, err := exec.LookPath("bash"); err == nil {
		return path
	}
	return "sh"
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// shellMissingHint is empty on unix: sh is always present.
const shellMissingHint = ""

//go:build windows

package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveShell finds a bash on Windows. pi looks in the Git for Windows install
// directories before searching PATH, because a bare "bash" on PATH is often the
// WSL stub, which cannot run in the project directory.
//
// The empty string means no shell was found; the caller then reports
// shellMissingHint.
func resolveShell() string {
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if base == "" {
			continue
		}
		candidate := filepath.Join(base, "Git", "bin", "bash.exe")
		if pathExists(candidate) {
			return candidate
		}
	}
	if path, err := exec.LookPath("bash.exe"); err == nil {
		return path
	}
	return ""
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// shellMissingHint is appended to the error when no bash is available.
var shellMissingHint = strings.Join([]string{
	"No bash shell found. Options:",
	"  1. Install Git for Windows: https://git-scm.com/download/win",
	"  2. Add your bash to PATH (Cygwin, MSYS2, etc.)",
}, "\n")

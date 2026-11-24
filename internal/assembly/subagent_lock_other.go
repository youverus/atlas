//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd

package assembly

import (
	"errors"
	"os"
)

func trySubagentLock(*os.File) (bool, error) {
	return false, errors.New("background subagents require file locking on this platform")
}

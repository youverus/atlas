//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package assembly

import (
	"errors"
	"os"
	"syscall"
)

func trySubagentLock(file *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return err == nil, err
		}
	}
}

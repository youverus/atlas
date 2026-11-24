//go:build darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func startPTYCommand(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	if err := ptyIoctl(ptm.Fd(), unix.TIOCPTYGRANT, 0); err != nil {
		_ = ptm.Close()
		return nil, fmt.Errorf("grantpt: %w", err)
	}
	if err := ptyIoctl(ptm.Fd(), unix.TIOCPTYUNLK, 0); err != nil {
		_ = ptm.Close()
		return nil, fmt.Errorf("unlockpt: %w", err)
	}

	var nameBuf [128]byte
	if err := ptyIoctl(ptm.Fd(), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&nameBuf[0]))); err != nil {
		_ = ptm.Close()
		return nil, fmt.Errorf("ptsname: %w", err)
	}
	slaveName := string(bytes.SplitN(nameBuf[:], []byte{0}, 2)[0])
	if slaveName == "" {
		_ = ptm.Close()
		return nil, errors.New("ptsname returned empty path")
	}

	pts, err := os.OpenFile(slaveName, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = ptm.Close()
		return nil, fmt.Errorf("open slave %s: %w", slaveName, err)
	}

	_ = resizePTY(ptm, cols, rows)

	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		_ = pts.Close()
		_ = ptm.Close()
		return nil, err
	}
	_ = pts.Close()
	return ptm, nil
}

func ptyIoctl(fd uintptr, req uint, arg uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func resizePTY(ptm *os.File, cols, rows int) error {
	if ptm == nil {
		return nil
	}
	if cols < 1 {
		cols = 1
	} else if cols > 65535 {
		cols = 65535
	}
	if rows < 1 {
		rows = 1
	} else if rows > 65535 {
		rows = 65535
	}
	return unix.IoctlSetWinsize(int(ptm.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})
}

func terminatePTYProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGHUP)
}

//go:build !darwin

package main

import (
	"errors"
	"os"
	"os/exec"
)

func startPTYCommand(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	return nil, errors.New("atlas-desktop: embedded terminal is only supported on macOS")
}

func resizePTY(ptm *os.File, cols, rows int) error {
	return errors.New("atlas-desktop: embedded terminal is only supported on macOS")
}

func terminatePTYProcess(cmd *exec.Cmd) {}

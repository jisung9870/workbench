//go:build windows

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

const (
	detachedProcess         = 0x00000008
	createNewProcessGroup   = 0x00000200
	processQueryLimitedInfo = 0x00001000
	errorInvalidParameter   = syscall.Errno(87)
)

func configureBackgroundCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup, HideWindow: true}
}

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err == nil {
		return true, syscall.CloseHandle(handle)
	}
	if errors.Is(err, errorInvalidParameter) {
		return false, nil
	}
	if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return true, nil
	}
	return false, err
}

func serverSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt)
}

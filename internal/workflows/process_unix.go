//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workflows

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareWorkflowCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateWorkflowProcessTree(process *os.Process) error {
	if process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

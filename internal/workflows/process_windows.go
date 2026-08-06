//go:build windows

package workflows

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func prepareWorkflowCommand(*exec.Cmd) {}

func terminateWorkflowProcessTree(process *os.Process) error {
	if process == nil {
		return os.ErrProcessDone
	}
	treeErr := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F").Run()
	if treeErr == nil {
		return nil
	}
	return errors.Join(treeErr, process.Kill())
}

package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

const capturedProcessWaitDelay = 250 * time.Millisecond

type ProcessRequest struct {
	Dir         string
	Name        string
	Args        []string
	Interactive bool
	Started     func(pid int) error
}

type ProcessResult struct {
	Command  []string
	ExitCode int
	Stdout   string
	Stderr   string
	PID      int
}

type Executor interface {
	LookPath(string) (string, error)
	Run(context.Context, ProcessRequest) (ProcessResult, error)
}

type OSExecutor struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (executor *OSExecutor) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (executor *OSExecutor) Run(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
	commandLine := append([]string{request.Name}, request.Args...)
	command := exec.CommandContext(ctx, request.Name, request.Args...)
	command.Dir = request.Dir
	result := ProcessResult{Command: commandLine, ExitCode: -1}
	var stdout, stderr bytes.Buffer
	if request.Interactive {
		command.Stdin = executor.Stdin
		command.Stdout = executor.Stdout
		command.Stderr = executor.Stderr
	} else {
		command.Stdout = &stdout
		command.Stderr = &stderr
		command.WaitDelay = capturedProcessWaitDelay
	}
	err := command.Start()
	if err == nil {
		result.PID = command.Process.Pid
		if request.Started != nil {
			if startErr := request.Started(result.PID); startErr != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				result.Stdout = stdout.String()
				result.Stderr = stderr.String()
				if command.ProcessState != nil {
					result.ExitCode = command.ProcessState.ExitCode()
				}
				return result, startErr
			}
		}
		err = command.Wait()
	}
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return result, errors.Join(contextErr, err)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return result, err
	}
	return result, err
}

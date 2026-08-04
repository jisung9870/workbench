package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

type ProcessRequest struct {
	Dir         string
	Name        string
	Args        []string
	Interactive bool
}

type ProcessResult struct {
	Command  []string
	ExitCode int
	Stdout   string
	Stderr   string
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
	}
	err := command.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return result, err
	}
	return result, err
}

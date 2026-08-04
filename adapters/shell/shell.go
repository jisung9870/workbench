package shell

import (
	"context"
	"fmt"
	"strings"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type Environment struct {
	GOOS   string
	Getenv func(string) string
}

type Adapter struct {
	executor backend.Executor
	env      Environment
}

func New(executor backend.Executor, environment Environment) *Adapter {
	return &Adapter{executor: executor, env: environment}
}

func (adapter *Adapter) Name() backend.Name { return backend.Shell }

func (adapter *Adapter) Detect(_ context.Context, _ backend.OpenRequest) backend.Capability {
	command, err := adapter.shellCommand()
	if err != nil {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: err.Error(), Capabilities: []string{}}
	}
	return backend.Capability{Backend: adapter.Name(), Available: true, Version: command, Capabilities: []string{"open_project"}}
}

func (adapter *Adapter) OpenProject(ctx context.Context, request backend.OpenRequest) (backend.OpenResult, error) {
	path, err := projects.CanonicalPath(request.Project.Path)
	if err != nil {
		return backend.OpenResult{Backend: adapter.Name(), Reference: "shell:" + request.Project.ID, ExitCode: -1}, err
	}
	command, err := adapter.shellCommand()
	if err != nil {
		return backend.OpenResult{Backend: adapter.Name(), Reference: "shell:" + request.Project.ID, ExitCode: -1}, err
	}
	process, runErr := adapter.executor.Run(ctx, backend.ProcessRequest{Dir: path, Name: command, Interactive: true})
	return backend.OpenResult{
		Backend: adapter.Name(), Reference: "shell:" + request.Project.ID, Command: process.Command,
		ExitCode: process.ExitCode, Stdout: process.Stdout, Stderr: process.Stderr,
	}, runErr
}

func (adapter *Adapter) shellCommand() (string, error) {
	candidates := []string{}
	if adapter.env.GOOS == "windows" {
		candidates = append(candidates, adapter.getenv("COMSPEC"), "pwsh.exe", "powershell.exe", "cmd.exe")
	} else {
		candidates = append(candidates, adapter.getenv("SHELL"), "/bin/sh", "sh")
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if resolved, err := adapter.executor.LookPath(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("no interactive shell was found")
}

func (adapter *Adapter) getenv(key string) string {
	if adapter.env.Getenv == nil {
		return ""
	}
	return adapter.env.Getenv(key)
}

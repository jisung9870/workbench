package tmux

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type Adapter struct {
	executor backend.Executor
	getenv   func(string) string
}

func New(executor backend.Executor, getenv func(string) string) *Adapter {
	return &Adapter{executor: executor, getenv: getenv}
}

func (adapter *Adapter) Name() backend.Name { return backend.Tmux }

func (adapter *Adapter) Detect(ctx context.Context, _ backend.OpenRequest) backend.Capability {
	command, err := adapter.executor.LookPath("tmux")
	if err != nil {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: "tmux executable was not found", Capabilities: []string{}}
	}
	detectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, versionErr := adapter.executor.Run(detectCtx, backend.ProcessRequest{Name: command, Args: []string{"-V"}})
	version := strings.TrimSpace(result.Stdout)
	if versionErr != nil {
		version = "unknown"
	}
	return backend.Capability{Backend: adapter.Name(), Available: true, Version: version, Capabilities: []string{"open_project", "list_sessions", "jump"}}
}

func (adapter *Adapter) OpenProject(ctx context.Context, request backend.OpenRequest) (backend.OpenResult, error) {
	path, err := projects.CanonicalPath(request.Project.Path)
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	command, err := adapter.executor.LookPath("tmux")
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	target := "=" + request.Project.ID
	if adapter.getenv != nil && adapter.getenv("TMUX") != "" {
		_, hasErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"has-session", "-t", target}})
		if hasErr != nil {
			created, createErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"new-session", "-d", "-s", request.Project.ID, "-c", path}})
			if createErr != nil {
				return adapter.result(request.Project.ID, created), fmt.Errorf("create tmux session: %w", createErr)
			}
		}
		switched, switchErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"switch-client", "-t", target}, Interactive: true})
		if switchErr != nil {
			return adapter.result(request.Project.ID, switched), fmt.Errorf("switch tmux client: %w", switchErr)
		}
		return adapter.result(request.Project.ID, switched), nil
	}
	opened, openErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"new-session", "-A", "-s", request.Project.ID, "-c", path}, Interactive: true})
	if openErr != nil {
		return adapter.result(request.Project.ID, opened), fmt.Errorf("open tmux session: %w", openErr)
	}
	return adapter.result(request.Project.ID, opened), nil
}

func (adapter *Adapter) result(projectID string, process backend.ProcessResult) backend.OpenResult {
	return backend.OpenResult{
		Backend: adapter.Name(), Reference: "tmux:" + projectID, Command: process.Command,
		ExitCode: process.ExitCode, Stdout: process.Stdout, Stderr: process.Stderr,
	}
}

package cmux

import (
	"context"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type Adapter struct {
	executor backend.Executor
	goos     string
}

func New(executor backend.Executor, goos string) *Adapter {
	return &Adapter{executor: executor, goos: goos}
}

func (adapter *Adapter) Name() backend.Name { return backend.CMUX }

func (adapter *Adapter) Detect(ctx context.Context, _ backend.OpenRequest) backend.Capability {
	if adapter.goos != "darwin" {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: "cmux is supported only on macOS", Capabilities: []string{}}
	}
	command, err := adapter.executor.LookPath("cmux")
	if err != nil {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: "cmux executable was not found", Capabilities: []string{}}
	}
	detectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, versionErr := adapter.executor.Run(detectCtx, backend.ProcessRequest{Name: command, Args: []string{"--version"}})
	version := strings.TrimSpace(result.Stdout)
	if versionErr != nil {
		version = "unknown"
	}
	return backend.Capability{Backend: adapter.Name(), Available: true, Version: version, Capabilities: []string{"open_project"}}
}

func (adapter *Adapter) OpenProject(ctx context.Context, request backend.OpenRequest) (backend.OpenResult, error) {
	path, err := projects.CanonicalPath(request.Project.Path)
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	command, err := adapter.executor.LookPath("cmux")
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	launchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	process, runErr := adapter.executor.Run(launchCtx, backend.ProcessRequest{Name: command, Args: []string{path}})
	return adapter.result(request.Project.ID, process), runErr
}

func (adapter *Adapter) result(projectID string, process backend.ProcessResult) backend.OpenResult {
	return backend.OpenResult{
		Backend: adapter.Name(), Surface: adapter.Name(), Reference: "cmux:" + projectID, Command: process.Command,
		ExitCode: process.ExitCode, Stdout: process.Stdout, Stderr: process.Stderr,
	}
}

package shell

import (
	"context"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeExecutor struct {
	request backend.ProcessRequest
}

func (executor *fakeExecutor) LookPath(name string) (string, error) { return name, nil }

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.request = request
	return backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}, nil
}

func TestOpenUsesWorkingDirectoryWithoutShellInterpolation(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, Environment{GOOS: "linux", Getenv: func(key string) string {
		if key == "SHELL" {
			return "/bin/test-shell"
		}
		return ""
	}})
	path := t.TempDir()
	result, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if executor.request.Dir != path || executor.request.Name != "/bin/test-shell" || len(executor.request.Args) != 0 || !executor.request.Interactive {
		t.Fatalf("unexpected process request: %#v", executor.request)
	}
	if result.Reference != "shell:alpha" {
		t.Fatalf("unexpected reference: %s", result.Reference)
	}
}

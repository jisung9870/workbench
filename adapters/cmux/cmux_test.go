package cmux

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
	if len(request.Args) == 1 && request.Args[0] == "--version" {
		return backend.ProcessResult{Command: []string{request.Name, "--version"}, ExitCode: 0, Stdout: "cmux 1.0\n"}, nil
	}
	return backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}, nil
}

func TestCMUXIsOptionalOutsideMacOS(t *testing.T) {
	adapter := New(&fakeExecutor{}, "linux")
	capability := adapter.Detect(context.Background(), backend.OpenRequest{})
	if capability.Available {
		t.Fatal("cmux must not be required outside macOS")
	}
}

func TestOpenPassesProjectPathAsOneArgument(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, "darwin")
	path := t.TempDir()
	canonicalPath, err := projects.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.request.Args) != 1 || executor.request.Args[0] != canonicalPath {
		t.Fatalf("unexpected cmux command: %#v", executor.request)
	}
}

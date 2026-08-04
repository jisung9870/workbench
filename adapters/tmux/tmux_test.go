package tmux

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeExecutor struct {
	requests []backend.ProcessRequest
}

func (executor *fakeExecutor) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.requests = append(executor.requests, request)
	result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
	if len(request.Args) > 0 && request.Args[0] == "has-session" {
		result.ExitCode = 1
		return result, errors.New("session missing")
	}
	return result, nil
}

func TestInsideTmuxCreatesMissingSessionThenSwitches(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	})
	path := t.TempDir()
	result, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 3 {
		t.Fatalf("unexpected calls: %#v", executor.requests)
	}
	wantCreate := []string{"new-session", "-d", "-s", "alpha", "-c", path}
	if !reflect.DeepEqual(executor.requests[1].Args, wantCreate) {
		t.Fatalf("unexpected create args: %v", executor.requests[1].Args)
	}
	if !reflect.DeepEqual(executor.requests[2].Args, []string{"switch-client", "-t", "=alpha"}) {
		t.Fatalf("unexpected switch args: %v", executor.requests[2].Args)
	}
	if result.Reference != "tmux:alpha" {
		t.Fatalf("unexpected reference: %s", result.Reference)
	}
}

func TestOutsideTmuxUsesAttachOrCreateArgumentArray(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, func(string) string { return "" })
	path := t.TempDir()
	_, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new-session", "-A", "-s", "alpha", "-c", path}
	if len(executor.requests) != 1 || !reflect.DeepEqual(executor.requests[0].Args, want) || !executor.requests[0].Interactive {
		t.Fatalf("unexpected process request: %#v", executor.requests)
	}
}

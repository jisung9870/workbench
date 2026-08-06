package tmux

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/sessions"
)

type fakeExecutor struct {
	requests []backend.ProcessRequest
	exists   bool
	path     string
	options  map[string]string
}

func (executor *fakeExecutor) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.requests = append(executor.requests, request)
	result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
	if executor.options == nil {
		executor.options = map[string]string{}
	}
	switch request.Args[0] {
	case "has-session":
		if !executor.exists {
			result.ExitCode = 1
			return result, errors.New("session missing")
		}
	case "new-session":
		executor.exists = true
		executor.path = argumentAfter(request.Args, "-c")
	case "set-option":
		executor.options[request.Args[3]] = request.Args[4]
	case "list-sessions":
		if executor.exists {
			result.Stdout = "alpha\t0\t1\n"
		}
	case "display-message":
		if !executor.exists {
			result.ExitCode = 1
			return result, errors.New("session missing")
		}
		result.Stdout = "0\t1\n"
	case "show-options":
		result.Stdout = executor.options[request.Args[4]] + "\n"
	case "list-panes":
		result.Stdout = executor.path + "\n"
	case "switch-client", "attach-session":
	default:
		return result, errors.New("unexpected tmux command")
	}
	return result, nil
}

func argumentAfter(args []string, option string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == option {
			return args[index+1]
		}
	}
	return ""
}

func findRequest(t *testing.T, requests []backend.ProcessRequest, command string) backend.ProcessRequest {
	t.Helper()
	for _, request := range requests {
		if len(request.Args) > 0 && request.Args[0] == command {
			return request
		}
	}
	t.Fatalf("command %s not found in %#v", command, requests)
	return backend.ProcessRequest{}
}

func TestInsideTmuxCreatesManagedSessionThenSwitches(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	})
	path := t.TempDir()
	canonicalPath, err := projects.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	wantCreate := []string{"new-session", "-d", "-s", "alpha", "-c", canonicalPath}
	if created := findRequest(t, executor.requests, "new-session"); !reflect.DeepEqual(created.Args, wantCreate) {
		t.Fatalf("unexpected create args: %v", created.Args)
	}
	if executor.options[sessions.ManagedOption] != "1" ||
		executor.options[sessions.ProjectIDOption] != "alpha" ||
		executor.options[sessions.ProjectPathOption] != canonicalPath {
		t.Fatalf("ownership metadata missing: %#v", executor.options)
	}
	last := executor.requests[len(executor.requests)-1]
	if !reflect.DeepEqual(last.Args, []string{"switch-client", "-t", "=alpha:"}) || !last.Interactive {
		t.Fatalf("unexpected switch request: %#v", last)
	}
	if result.Reference != "tmux:alpha" || result.Session != backend.Tmux || result.Surface != backend.Tmux {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestOutsideTmuxCreatesThenAttachesExactSession(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, func(string) string { return "" })
	path := t.TempDir()
	_, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	last := executor.requests[len(executor.requests)-1]
	want := []string{"attach-session", "-t", "=alpha:"}
	if !reflect.DeepEqual(last.Args, want) || !last.Interactive {
		t.Fatalf("unexpected attach request: %#v", last)
	}
}

func TestExistingLegacySessionIsAttachedWithoutImplicitAdopt(t *testing.T) {
	path := t.TempDir()
	executor := &fakeExecutor{exists: true, path: path, options: map[string]string{}}
	adapter := New(executor, func(string) string { return "" })
	if _, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}}); err != nil {
		t.Fatal(err)
	}
	for _, request := range executor.requests {
		if len(request.Args) > 0 && request.Args[0] == "set-option" {
			t.Fatal("legacy session was adopted implicitly")
		}
	}
}

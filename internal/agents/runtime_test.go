package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeExecutor struct {
	lookups map[string]string
	run     func(backend.ProcessRequest) (backend.ProcessResult, error)
	calls   []backend.ProcessRequest
}

func (executor *fakeExecutor) LookPath(name string) (string, error) {
	if path := executor.lookups[name]; path != "" {
		return path, nil
	}
	return "", fmt.Errorf("missing %s", name)
}

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.calls = append(executor.calls, request)
	if executor.run != nil {
		return executor.run(request)
	}
	return backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}, nil
}

func TestTmuxStopRequiresMatchingOwnershipMetadata(t *testing.T) {
	task := Task{ID: "task-1", Backend: backend.Tmux, BackendRef: "tmux:%7", BackendDetails: map[string]string{"pane": "%7", "session": "alpha"}}
	executor := &fakeExecutor{lookups: map[string]string{"tmux": "/usr/bin/tmux"}}
	executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
		result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
		if len(request.Args) > 0 && request.Args[0] == "display-message" {
			result.Stdout = "different-task\n"
		}
		return result, nil
	}
	err := NewTmuxRuntime(executor, nil).Stop(context.Background(), task)
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("ownership mismatch was not rejected: %v", err)
	}
	for _, call := range executor.calls {
		if len(call.Args) > 0 && call.Args[0] == "kill-pane" {
			t.Fatal("tmux pane was killed after ownership mismatch")
		}
	}
}

func TestTmuxStopKillsOnlyVerifiedPane(t *testing.T) {
	task := Task{ID: "task-1", Backend: backend.Tmux, BackendRef: "tmux:%7", BackendDetails: map[string]string{"pane": "%7", "session": "alpha"}}
	executor := &fakeExecutor{lookups: map[string]string{"tmux": "/usr/bin/tmux"}}
	executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
		result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
		if len(request.Args) > 0 && request.Args[0] == "display-message" {
			result.Stdout = task.ID + "\n"
		}
		return result, nil
	}
	if err := NewTmuxRuntime(executor, nil).Stop(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	last := executor.calls[len(executor.calls)-1]
	if strings.Join(last.Args, " ") != "kill-pane -t =%7" {
		t.Fatalf("unexpected stop command: %#v", last.Args)
	}
}

func TestCMUXStopRequiresRegisteredWorkspaceToExist(t *testing.T) {
	task := Task{ID: "task-1", Backend: backend.CMUX, BackendRef: "cmux:workspace:9", BackendDetails: map[string]string{"workspace": "workspace:9"}}
	executor := &fakeExecutor{lookups: map[string]string{"cmux": "/usr/local/bin/cmux"}}
	executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
		return backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), Stdout: `{"workspaces":[{"id":"workspace:2"}]}`, ExitCode: 0}, nil
	}
	err := NewCMUXRuntime(executor, "darwin").Stop(context.Background(), task)
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("missing workspace was not rejected: %v", err)
	}
	for _, call := range executor.calls {
		if len(call.Args) > 0 && call.Args[0] == "close-workspace" {
			t.Fatal("cmux workspace close ran without an exact list match")
		}
	}
}

func TestCMUXLaunchRegistersWorkspaceAndTargetsSurface(t *testing.T) {
	executor := &fakeExecutor{lookups: map[string]string{"cmux": "/usr/local/bin/cmux"}}
	executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
		result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
		switch request.Args[0] {
		case "new-workspace":
			result.Stdout = `{"workspace":{"id":"workspace:9"}}`
		case "list-panels":
			result.Stdout = `{"surfaces":[{"id":"surface:3"}]}`
		}
		return result, nil
	}
	references := []string{}
	details := map[string]string{}
	result, err := NewCMUXRuntime(executor, "darwin").Launch(context.Background(), LaunchRequest{
		Task: Task{ID: "task-1", AgentKind: "codex", CWD: "/tmp/project"}, Executable: "/usr/bin/codex",
		OnStarted: func(reference string, current map[string]string, _ int) error {
			references = append(references, reference)
			details = current
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Waited || len(references) != 2 || references[1] != "cmux:workspace:9" || details["surface"] != "surface:3" {
		t.Fatalf("cmux ownership was not registered: result=%#v refs=%#v details=%#v", result, references, details)
	}
	last := executor.calls[len(executor.calls)-1]
	if len(last.Args) != 4 || last.Args[0] != "send" || last.Args[2] != "surface:3" || !strings.Contains(last.Args[3], "exec '/usr/bin/codex'") {
		t.Fatalf("cmux did not target the created surface safely: %#v", last.Args)
	}
}

func TestWindowsTerminalLaunchPreservesProfileAndRegistersLaunchOnlyReference(t *testing.T) {
	executor := &fakeExecutor{lookups: map[string]string{"wt.exe": "wt.exe"}}
	started := ""
	result, err := NewWindowsTerminalRuntime(executor, "windows", nil).Launch(context.Background(), LaunchRequest{
		Task:    Task{ID: "task-1", AgentKind: "claude", CWD: `C:\work\alpha`},
		Project: projects.Project{ID: "alpha"}, Profile: config.Profile{WindowsTerminalProfile: "PowerShell"},
		Executable: `C:\bin\claude.exe`,
		OnStarted: func(reference string, _ map[string]string, _ int) error {
			started = reference
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Waited || started != "windows-terminal:task-1" {
		t.Fatalf("unexpected Windows Terminal launch result: %#v ref=%q", result, started)
	}
	args := strings.Join(executor.calls[0].Args, "|")
	if !strings.Contains(args, "--profile|PowerShell") || !strings.Contains(args, `--startingDirectory|C:\work\alpha|C:\bin\claude.exe`) {
		t.Fatalf("profile or native command was lost: %s", args)
	}
}

func TestShellStopRefusesPIDGuess(t *testing.T) {
	err := NewShellRuntime(&fakeExecutor{}).Stop(context.Background(), Task{BackendRef: "process:123"})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("shell stop should be unavailable: %v", err)
	}
}

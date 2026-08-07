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
	verified := executor.calls[len(executor.calls)-2]
	if strings.Join(verified.Args, " ") != "display-message -p -t %7 #{@workbench_task_id}" {
		t.Fatalf("unexpected ownership command: %#v", verified.Args)
	}
	last := executor.calls[len(executor.calls)-1]
	if strings.Join(last.Args, " ") != "kill-pane -t %7" {
		t.Fatalf("unexpected stop command: %#v", last.Args)
	}
}

func TestTmuxAliveTreatsOwnershipMismatchAsStopped(t *testing.T) {
	task := Task{ID: "task-1", Backend: backend.Tmux, BackendRef: "tmux:%7", BackendDetails: map[string]string{"pane": "%7", "session": "alpha"}}
	executor := &fakeExecutor{lookups: map[string]string{"tmux": "/usr/bin/tmux"}}
	executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
		result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
		if len(request.Args) > 0 && request.Args[0] == "display-message" {
			result.Stdout = "different-task\n"
		}
		return result, nil
	}
	alive, err := NewTmuxRuntime(executor, nil).Alive(context.Background(), task)
	if err != nil || alive {
		t.Fatalf("ownership mismatch must reconcile to stopped: alive=%v err=%v", alive, err)
	}
}

func TestTmuxAliveRejectsInvalidPaneReference(t *testing.T) {
	task := Task{ID: "task-1", Backend: backend.Tmux, BackendRef: "tmux:%7", BackendDetails: map[string]string{"pane": "7", "session": "alpha"}}
	_, err := NewTmuxRuntime(&fakeExecutor{}, nil).Alive(context.Background(), task)
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("invalid pane reference was not rejected: %v", err)
	}
}

func TestTmuxLaunchSetsSessionAndPaneOwnershipMetadata(t *testing.T) {
	path := t.TempDir()
	canonicalPath, err := projects.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{lookups: map[string]string{"tmux": "/usr/bin/tmux"}}
	sessionCreated := false
	sessionOptions := map[string]string{}
	executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
		result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
		switch request.Args[0] {
		case "has-session":
			if !sessionCreated {
				result.ExitCode = 1
				return result, errors.New("session missing")
			}
		case "new-session":
			sessionCreated = true
		case "set-option":
			if len(request.Args) > 1 && request.Args[1] != "-p" {
				sessionOptions[request.Args[3]] = request.Args[4]
			}
		case "list-sessions":
			if sessionCreated {
				result.Stdout = "alpha\t0\t1\n"
			}
		case "show-options":
			result.Stdout = sessionOptions[request.Args[4]] + "\n"
		case "list-panes":
			result.Stdout = canonicalPath + "\n"
		case "new-window":
			result.Stdout = "%7\n"
		}
		return result, nil
	}
	started := ""
	paneMetadataAtStart := 0
	result, err := NewTmuxRuntime(executor, nil).Launch(context.Background(), LaunchRequest{
		Task:       Task{ID: "task-1", AgentKind: "codex", CWD: canonicalPath},
		Project:    projects.Project{ID: "alpha", Path: path},
		Executable: "/usr/bin/codex",
		OnStarted: func(reference string, _ map[string]string, _ int) error {
			started = reference
			for _, call := range executor.calls {
				if len(call.Args) > 1 && call.Args[0] == "set-option" && call.Args[1] == "-p" {
					paneMetadataAtStart++
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Waited || started != "tmux:%7" {
		t.Fatalf("unexpected tmux launch result: %#v ref=%q", result, started)
	}
	paneMetadataCalls := 0
	for _, call := range executor.calls {
		if len(call.Args) > 1 && call.Args[0] == "set-option" && call.Args[1] == "-p" {
			paneMetadataCalls++
			if len(call.Args) < 4 || call.Args[3] != "%7" {
				t.Fatalf("metadata did not target raw pane ID: %#v", call.Args)
			}
		}
	}
	if paneMetadataCalls != 2 || paneMetadataAtStart != 2 {
		t.Fatalf("unexpected pane metadata timing: total=%d at_start=%d", paneMetadataCalls, paneMetadataAtStart)
	}
	if sessionOptions["@workbench_managed"] != "1" || sessionOptions["@workbench_project_id"] != "alpha" || sessionOptions["@workbench_project_path"] != canonicalPath {
		t.Fatalf("session ownership metadata missing: %#v", sessionOptions)
	}
}

func TestTmuxJumpTargetsRawPaneID(t *testing.T) {
	task := Task{ID: "task-1", Backend: backend.Tmux, BackendRef: "tmux:%7", BackendDetails: map[string]string{"pane": "%7", "session": "alpha"}}

	for _, test := range []struct {
		name     string
		tmuxEnv  string
		expected string
	}{
		{name: "inside tmux", tmuxEnv: "/tmp/tmux,1,0", expected: "switch-client -t %7"},
		{name: "outside tmux", expected: "attach-session -t =alpha ; select-pane -t %7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeExecutor{lookups: map[string]string{"tmux": "/usr/bin/tmux"}}
			executor.run = func(request backend.ProcessRequest) (backend.ProcessResult, error) {
				result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
				if len(request.Args) > 0 && request.Args[0] == "display-message" {
					result.Stdout = task.ID + "\n"
				}
				return result, nil
			}
			getenv := func(string) string { return test.tmuxEnv }
			if err := NewTmuxRuntime(executor, getenv).Jump(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			last := executor.calls[len(executor.calls)-1]
			if strings.Join(last.Args, " ") != test.expected {
				t.Fatalf("unexpected jump command: %#v", last.Args)
			}
		})
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
	if !strings.Contains(args, "--window|last|new-tab") || !strings.Contains(args, "--profile|PowerShell") || !strings.Contains(args, `--startingDirectory|C:\work\alpha|C:\bin\claude.exe`) {
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

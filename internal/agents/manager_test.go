package agents

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/worktrees"
)

type fakeProjects struct{ project projects.Project }

func (store fakeProjects) Show(id string) (projects.Project, bool, error) {
	return store.project, id == store.project.ID, nil
}

type fakeWorktrees struct{}

func (fakeWorktrees) Resolve(context.Context, string, string) (worktrees.Item, error) {
	return worktrees.Item{}, errors.New("unexpected worktree resolution")
}

type fakeRuntime struct {
	name      backend.Name
	launchErr error
}

func (runtime fakeRuntime) Name() backend.Name { return runtime.name }
func (runtime fakeRuntime) Detect(context.Context, backend.OpenRequest) backend.Capability {
	return backend.Capability{Backend: runtime.name, Available: true, Capabilities: []string{"agents_start"}}
}
func (runtime fakeRuntime) Launch(_ context.Context, request LaunchRequest) (LaunchResult, error) {
	if runtime.launchErr != nil {
		return LaunchResult{Command: []string{request.Executable}, ExitCode: 9}, runtime.launchErr
	}
	if err := request.OnStarted("tmux:%8", map[string]string{"pane": "%8", "session": request.Project.ID}, 0); err != nil {
		return LaunchResult{ExitCode: -1}, err
	}
	return LaunchResult{Command: []string{request.Executable}, ExitCode: 0}, nil
}
func (fakeRuntime) Alive(context.Context, Task) (bool, error) { return true, nil }
func (fakeRuntime) Jump(context.Context, Task) error          { return nil }
func (fakeRuntime) Stop(context.Context, Task) error          { return nil }

func TestManagerRegistersStartingBeforeLaunchAndThenRunning(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), AgentsFile: filepath.Join(root, "state", "agents.json"), BackupsDir: filepath.Join(root, "state", "backups"), ConfigFile: filepath.Join(root, "config.toml"), ProfilesDir: filepath.Join(root, "profiles")}
	state := NewStateStore(paths)
	executor := &fakeExecutor{lookups: map[string]string{"codex": "/usr/bin/codex"}}
	project := projects.Project{ID: "alpha", Path: root, RepoRoot: root, DefaultBackend: "auto"}
	manager := NewManager(paths, fakeProjects{project}, fakeWorktrees{}, state, executor, backend.Environment{GOOS: "linux"}, fakeRuntime{name: backend.Tmux})
	fixed := time.Date(2026, 8, 5, 2, 3, 4, 0, time.UTC)
	manager.now = func() time.Time { return fixed }
	manager.newID = func(time.Time) (string, error) { return "task-fixed", nil }
	task, _, err := manager.Start(context.Background(), StartRequest{ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != Running || task.BackendRef != "tmux:%8" || task.StateSource != SourceRegistry {
		t.Fatalf("unexpected registered task: %#v", task)
	}
	stored, found, err := state.Show(task.ID)
	if err != nil || !found || stored.State != Running {
		t.Fatalf("task was not immediately queryable: %#v found=%v err=%v", stored, found, err)
	}
}

func TestManagerRecordsLaunchFailure(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups"), ConfigFile: filepath.Join(root, "config.toml"), ProfilesDir: filepath.Join(root, "profiles")}
	state := NewStateStore(paths)
	executor := &fakeExecutor{lookups: map[string]string{"claude": "/usr/bin/claude"}}
	project := projects.Project{ID: "alpha", Path: root, RepoRoot: root, DefaultBackend: "auto"}
	manager := NewManager(paths, fakeProjects{project}, fakeWorktrees{}, state, executor, backend.Environment{GOOS: "linux"}, fakeRuntime{name: backend.Tmux, launchErr: errors.New("launch failed")})
	manager.newID = func(time.Time) (string, error) { return "task-failed", nil }
	task, _, err := manager.Start(context.Background(), StartRequest{ProjectID: "alpha", AgentKind: "claude", Backend: backend.Tmux})
	if err == nil || task.State != Failed || task.ExitCode == nil || *task.ExitCode != 9 {
		t.Fatalf("launch failure was not recorded: task=%#v err=%v", task, err)
	}
	stored, found, loadErr := state.Show(task.ID)
	if loadErr != nil || !found || stored.State != Failed {
		t.Fatalf("failed task is not queryable: %#v found=%v err=%v", stored, found, loadErr)
	}
}

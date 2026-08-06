package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/dashboard"
	"github.com/jisung9870/workbench/internal/projects"
)

func TestDashboardRejectsInvalidOptionsBeforeListening(t *testing.T) {
	for _, args := range [][]string{
		{"dashboard", "--open", "external"},
		{"dashboard", "--port", "65536"},
		{"dashboard", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitArgument {
			t.Fatalf("expected argument error for %v, got %d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestDashboardServiceRejectsUnknownAction(t *testing.T) {
	_, err := (&dashboardService{}).Execute(context.Background(), dashboard.ActionRequest{Action: "shell", ProjectID: "alpha"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("unexpected action error: %v", err)
	}
}

func TestDashboardClearAgentHistoryPrunesOnlyTerminalTasks(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups"), ProjectsFile: filepath.Join(root, "projects.toml")}
	if _, _, err := projects.NewStore(paths).Add(root, "alpha", "personal"); err != nil {
		t.Fatal(err)
	}
	store := agents.NewStateStore(paths)
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	for _, task := range []agents.Task{
		{ID: "task-running", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%1", State: agents.Running, StateSource: agents.SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
		{ID: "task-stopped", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%2", State: agents.Stopped, StateSource: agents.SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
	} {
		if _, err := store.Create(task); err != nil {
			t.Fatal(err)
		}
	}

	result, err := (&dashboardService{paths: paths}).Execute(context.Background(), dashboard.ActionRequest{Action: "clear_agent_history", ProjectID: "alpha", TaskIDs: []string{"task-stopped"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "cleared 1 terminal task records") || !strings.Contains(result.Message, "backup ") {
		t.Fatalf("unexpected result: %#v", result)
	}
	tasks, err := store.List("alpha")
	if err != nil || len(tasks) != 1 || tasks[0].ID != "task-running" {
		t.Fatalf("unexpected remaining tasks: %#v err=%v", tasks, err)
	}
}

func TestDashboardClearAgentHistoryRejectsMixedActionFields(t *testing.T) {
	_, err := (&dashboardService{}).Execute(context.Background(), dashboard.ActionRequest{Action: "clear_agent_history", ProjectID: "alpha", TaskID: "task-1", TaskIDs: []string{"task-1"}})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("unexpected action error: %v", err)
	}
}

func TestDashboardClearAgentHistoryRejectsStaleTaskSet(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups"), ProjectsFile: filepath.Join(root, "projects.toml")}
	if _, _, err := projects.NewStore(paths).Add(root, "alpha", "personal"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	store := agents.NewStateStore(paths)
	if _, err := store.Create(agents.Task{ID: "task-stopped", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%2", State: agents.Stopped, StateSource: agents.SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now}); err != nil {
		t.Fatal(err)
	}

	_, err := (&dashboardService{paths: paths}).Execute(context.Background(), dashboard.ActionRequest{Action: "clear_agent_history", ProjectID: "alpha", TaskIDs: []string{"task-stopped", "task-stale"}})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Status != http.StatusConflict {
		t.Fatalf("stale history did not return conflict: %v", err)
	}
	if _, found, loadErr := store.Show("task-stopped"); loadErr != nil || !found {
		t.Fatalf("stale clear changed registry: found=%v err=%v", found, loadErr)
	}
}

type changeExecutor struct {
	result backend.ProcessResult
	args   []string
}

func (executor *changeExecutor) LookPath(string) (string, error) { return "git", nil }
func (executor *changeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.args = request.Args
	return executor.result, nil
}

func TestProjectChangesUsesGitArgumentArrayAndParsesPorcelain(t *testing.T) {
	executor := &changeExecutor{result: backend.ProcessResult{Stdout: "## feature/dashboard...origin/feature/dashboard\n M internal/dashboard.go\nR  old.txt -> new.txt\n?? notes.md\n"}}
	summary := projectChanges(context.Background(), executor, projects.Project{ID: "alpha", RepoRoot: "/repo with spaces"})
	wantArgs := []string{"-C", "/repo with spaces", "status", "--porcelain=v1", "--branch", "--untracked-files=normal"}
	if !reflect.DeepEqual(executor.args, wantArgs) {
		t.Fatalf("git arguments were not preserved: %v", executor.args)
	}
	if summary.Branch != "feature/dashboard" || !summary.Dirty || summary.Changed != 3 {
		t.Fatalf("unexpected change summary: %#v", summary)
	}
	if got := strings.Join(summary.ChangedFiles, ","); got != "internal/dashboard.go,new.txt,notes.md" {
		t.Fatalf("unexpected changed files: %s", got)
	}
}

type dashboardStubAdapter struct {
	name      backend.Name
	available bool
	detects   int
}

func (adapter *dashboardStubAdapter) Name() backend.Name { return adapter.name }

func (adapter *dashboardStubAdapter) Detect(context.Context, backend.OpenRequest) backend.Capability {
	adapter.detects++
	return backend.Capability{Backend: adapter.name, Available: adapter.available, Reason: "test unavailable"}
}

func (adapter *dashboardStubAdapter) OpenProject(context.Context, backend.OpenRequest) (backend.OpenResult, error) {
	return backend.OpenResult{Backend: adapter.name}, nil
}

func TestDashboardOpenAutoSkipsInteractiveTmuxPreference(t *testing.T) {
	cmux := &dashboardStubAdapter{name: backend.CMUX, available: true}
	tmux := &dashboardStubAdapter{name: backend.Tmux, available: true}
	shell := &dashboardStubAdapter{name: backend.Shell, available: true}
	environment := backend.Environment{GOOS: "darwin", Getenv: func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	}}
	profile := config.DefaultProfile()
	profile.PreferCurrentTmux = true
	profile.BackendPriority = []string{"tmux", "cmux", "shell"}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha", DefaultBackend: "auto"}, Profile: profile}
	registry := backend.NewRegistry(environment, cmux, tmux, shell)

	selection, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Auto, environment)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != backend.CMUX {
		t.Fatalf("expected Dashboard-compatible cmux, got %s", selection.Adapter.Name())
	}
	if tmux.detects != 0 || shell.detects != 0 {
		t.Fatalf("interactive backends were probed: tmux=%d shell=%d", tmux.detects, shell.detects)
	}
}

func TestDashboardOpenUsesWindowsTerminalPlatformFallback(t *testing.T) {
	windowsTerminal := &dashboardStubAdapter{name: backend.WindowsTerminal, available: true}
	tmux := &dashboardStubAdapter{name: backend.Tmux, available: true}
	environment := backend.Environment{GOOS: "windows", Getenv: func(string) string { return "" }}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha", DefaultBackend: "auto"}, Profile: config.DefaultProfile()}
	registry := backend.NewRegistry(environment, windowsTerminal, tmux)

	selection, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Auto, environment)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != backend.WindowsTerminal || selection.Surface != backend.WindowsTerminal || selection.Session != "" {
		t.Fatalf("expected Windows Terminal, got %s", selection.Adapter.Name())
	}
}

func TestDashboardOpenUsesTmuxSessionViaWindowsTerminalInWSL(t *testing.T) {
	windowsTerminal := &dashboardStubAdapter{name: backend.WindowsTerminal, available: true}
	tmux := &dashboardStubAdapter{name: backend.Tmux, available: true}
	environment := backend.Environment{GOOS: "linux", Getenv: func(key string) string {
		switch key {
		case "WSL_DISTRO_NAME":
			return "Ubuntu"
		case "TMUX":
			return "/tmp/tmux,1,0"
		default:
			return ""
		}
	}}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha", DefaultBackend: "auto"}, Profile: config.DefaultProfile()}
	registry := backend.NewRegistry(environment, windowsTerminal, tmux)

	selection, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Auto, environment)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != backend.WindowsTerminal || selection.Session != backend.Tmux || selection.Surface != backend.WindowsTerminal {
		t.Fatalf("expected tmux via Windows Terminal in WSL, got %#v", selection)
	}
	if tmux.detects != 1 {
		t.Fatalf("tmux capability should be checked without opening or switching a client: detects=%d", tmux.detects)
	}
}

func TestDashboardOpenSkipsPriorityCMUXOverSSH(t *testing.T) {
	cmux := &dashboardStubAdapter{name: backend.CMUX, available: true}
	environment := backend.Environment{GOOS: "darwin", Getenv: func(key string) string {
		if key == "SSH_CONNECTION" {
			return "client server"
		}
		return ""
	}}
	profile := config.DefaultProfile()
	profile.BackendPriority = []string{"cmux", "tmux", "shell"}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha", DefaultBackend: "auto"}, Profile: profile}
	registry := backend.NewRegistry(environment, cmux)

	_, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Auto, environment)
	var unavailable *backend.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected no compatible backend, got %v", err)
	}
	if cmux.detects != 0 {
		t.Fatal("priority-sourced cmux was probed over SSH")
	}
}

func TestDashboardOpenRejectsExplicitTmuxWithoutSupportedSurface(t *testing.T) {
	tmux := &dashboardStubAdapter{name: backend.Tmux, available: true}
	environment := backend.Environment{GOOS: "darwin", Getenv: func(string) string { return "" }}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha"}, Profile: config.DefaultProfile()}
	registry := backend.NewRegistry(environment, tmux)

	_, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Tmux, environment)
	if err == nil || !strings.Contains(err.Error(), "requires WSL with Windows Terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

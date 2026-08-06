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

	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/dashboard"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/workflows"
)

type fakeWorkflowRuntime struct {
	catalog          []workflows.Availability
	history          []workflows.Result
	result           workflows.Result
	runID, projectID string
	err              error
	jumpID           string
}

func (f *fakeWorkflowRuntime) Catalog(context.Context, string) ([]workflows.Availability, error) {
	return f.catalog, nil
}
func (f *fakeWorkflowRuntime) History(string) ([]workflows.Result, error) { return f.history, nil }
func (f *fakeWorkflowRuntime) Launch(_ context.Context, workflowID, projectID string) (workflows.Result, string, error) {
	f.runID, f.projectID = workflowID, projectID
	return f.result, "", f.err
}
func (f *fakeWorkflowRuntime) Jump(_ context.Context, id string, _ bool, _ func(string) string) error {
	f.jumpID = id
	return f.err
}

type fakeTmuxRuntime struct {
	snapshot tmuxadapter.Snapshot
	jumped   string
	jumpErr  error
}

func (runtime *fakeTmuxRuntime) Snapshot(context.Context) tmuxadapter.Snapshot {
	return runtime.snapshot
}
func (runtime *fakeTmuxRuntime) Jump(_ context.Context, paneID string, allowAttach bool) error {
	if allowAttach {
		return errors.New("dashboard must not request interactive attach")
	}
	runtime.jumped = paneID
	return runtime.jumpErr
}

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

func TestDashboardRunsOnlyTypedWorkflowFields(t *testing.T) {
	runtime := &fakeWorkflowRuntime{result: workflows.Result{ID: "run-1", WorkflowID: workflows.ProjectTest, ProjectID: "alpha", Status: workflows.Succeeded}}
	service := &dashboardService{workflows: runtime}
	result, err := service.Execute(context.Background(), dashboard.ActionRequest{Action: "run_workflow", ProjectID: "alpha", WorkflowID: workflows.ProjectTest})
	if err != nil || runtime.runID != workflows.ProjectTest || runtime.projectID != "alpha" || result.WorkflowRun == nil {
		t.Fatalf("typed workflow failed: result=%#v runtime=%#v err=%v", result, runtime, err)
	}
	_, err = service.Execute(context.Background(), dashboard.ActionRequest{Action: "run_workflow", ProjectID: "alpha", WorkflowID: workflows.ProjectTest, Backend: "shell"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("mixed workflow fields accepted: %v", err)
	}
}

func TestDashboardJumpsManagedWorkflowTask(t *testing.T) {
	runtime := &fakeWorkflowRuntime{}
	result, err := (&dashboardService{workflows: runtime}).Execute(context.Background(), dashboard.ActionRequest{Action: "jump_task", TaskID: "run-100-abcdef12"})
	if err != nil || runtime.jumpID != "run-100-abcdef12" || !strings.Contains(result.Message, "workflow task") {
		t.Fatalf("workflow jump failed: %#v id=%s err=%v", result, runtime.jumpID, err)
	}
}

func TestDashboardRejectsWorkflowFieldOnOtherActions(t *testing.T) {
	_, err := (&dashboardService{}).Execute(context.Background(), dashboard.ActionRequest{Action: "jump_pane", PaneID: "%1", WorkflowID: workflows.ProjectTest})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("workflow field was accepted by another action: %v", err)
	}
}

func TestDashboardServiceRejectsUnknownAction(t *testing.T) {
	_, err := (&dashboardService{}).Execute(context.Background(), dashboard.ActionRequest{Action: "shell", ProjectID: "alpha"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("unexpected action error: %v", err)
	}
}

func TestDashboardJumpPaneUsesTypedStableIdentifierAction(t *testing.T) {
	runtime := &fakeTmuxRuntime{}
	result, err := (&dashboardService{tmux: runtime}).Execute(context.Background(), dashboard.ActionRequest{Action: "jump_pane", PaneID: "%12"})
	if err != nil || runtime.jumped != "%12" || !strings.Contains(result.Message, "%12") {
		t.Fatalf("unexpected jump result=%#v jumped=%q err=%v", result, runtime.jumped, err)
	}
	_, err = (&dashboardService{tmux: runtime}).Execute(context.Background(), dashboard.ActionRequest{Action: "jump_pane", PaneID: "%12", Backend: "shell"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("mixed jump fields were accepted: %v", err)
	}
}

func TestDashboardObservedTaskJumpRevalidatesSnapshot(t *testing.T) {
	runtime := &fakeTmuxRuntime{snapshot: tmuxadapter.Snapshot{Available: true, Sessions: []tmuxadapter.Session{{
		ID: "$1", Name: "alpha", Windows: []tmuxadapter.Window{{ID: "@1", Panes: []tmuxadapter.Pane{{ID: "%12", CurrentCommand: "codex", CurrentPath: "/repo"}}}},
	}}}}
	result, err := (&dashboardService{tmux: runtime}).Execute(context.Background(), dashboard.ActionRequest{Action: "jump_task", TaskID: "tmux:%12"})
	if err != nil || runtime.jumped != "%12" || !strings.Contains(result.Message, "tmux:%12") {
		t.Fatalf("observed task was not revalidated and jumped: result=%#v jumped=%q err=%v", result, runtime.jumped, err)
	}

	runtime.snapshot.Sessions[0].Windows[0].Panes[0].CurrentCommand = "zsh"
	_, err = (&dashboardService{tmux: runtime}).Execute(context.Background(), dashboard.ActionRequest{Action: "jump_task", TaskID: "tmux:%12"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "OBSERVED_TASK_UNAVAILABLE" {
		t.Fatalf("stale observed task was accepted: %v", err)
	}
}

func TestDashboardRefusesObservedTaskStop(t *testing.T) {
	_, err := (&dashboardService{}).Execute(context.Background(), dashboard.ActionRequest{Action: "stop_task", TaskID: "tmux:%12"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "TASK_UNMANAGED" || actionErr.Status != http.StatusConflict {
		t.Fatalf("observed stop was not refused explicitly: %v", err)
	}
}

func TestDashboardSnapshotIncludesOptionalTmuxUnavailable(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups"),
		ProjectsFile: filepath.Join(root, "projects.toml"), ConfigFile: filepath.Join(root, "config.toml"),
		ProfilesDir: filepath.Join(root, "profiles"), CompatibilityDir: filepath.Join(root, "compatibility"),
	}
	runtime := &fakeTmuxRuntime{snapshot: tmuxadapter.Snapshot{Available: false, Reason: "no server running", Sessions: []tmuxadapter.Session{}}}
	snapshot, err := (&dashboardService{paths: paths, tmux: runtime}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Tmux.Available || snapshot.Tmux.Reason != "no server running" {
		t.Fatalf("optional tmux state was not preserved: %#v", snapshot.Tmux)
	}
	if snapshot.Tasks == nil || len(snapshot.Tasks) != 0 {
		t.Fatalf("tmux unavailability should produce an empty optional projection: %#v", snapshot.Tasks)
	}
}

func TestDashboardSnapshotIncludesWorkflowCatalogAndBoundedHistory(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups"), ProjectsFile: filepath.Join(root, "projects.toml"), ConfigFile: filepath.Join(root, "config.toml"), ProfilesDir: filepath.Join(root, "profiles"), CompatibilityDir: filepath.Join(root, "compatibility")}
	if _, _, err := projects.NewStore(paths).Add(root, "alpha", "personal"); err != nil {
		t.Fatal(err)
	}
	workflowRuntime := &fakeWorkflowRuntime{catalog: []workflows.Availability{{Definition: workflows.Definition{ID: workflows.ProjectTest}, ProjectID: "alpha", Status: "available"}}, history: []workflows.Result{{ID: "run-1", WorkflowID: workflows.ProjectTest, ProjectID: "alpha", Status: workflows.Succeeded}}}
	snapshot, err := (&dashboardService{paths: paths, tmux: &fakeTmuxRuntime{snapshot: tmuxadapter.Snapshot{Available: false, Sessions: []tmuxadapter.Session{}}}, workflows: workflowRuntime}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workflows) != 1 || snapshot.Workflows[0].ID != workflows.ProjectTest || len(snapshot.WorkflowHistory) != 1 || snapshot.WorkflowHistory[0].ID != "run-1" {
		t.Fatalf("workflow snapshot missing: catalog=%#v history=%#v", snapshot.Workflows, snapshot.WorkflowHistory)
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
	if selection.Adapter.Name() != backend.WindowsTerminal {
		t.Fatalf("expected Windows Terminal, got %s", selection.Adapter.Name())
	}
}

func TestDashboardOpenUsesWindowsTerminalInWSL(t *testing.T) {
	windowsTerminal := &dashboardStubAdapter{name: backend.WindowsTerminal, available: true}
	environment := backend.Environment{GOOS: "linux", Getenv: func(key string) string {
		if key == "WSL_DISTRO_NAME" {
			return "Ubuntu"
		}
		return ""
	}}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha", DefaultBackend: "auto"}, Profile: config.DefaultProfile()}
	registry := backend.NewRegistry(environment, windowsTerminal)

	selection, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Auto, environment)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != backend.WindowsTerminal {
		t.Fatalf("expected Windows Terminal in WSL, got %s", selection.Adapter.Name())
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

func TestDashboardOpenRejectsExplicitInteractiveBackend(t *testing.T) {
	tmux := &dashboardStubAdapter{name: backend.Tmux, available: true}
	environment := backend.Environment{GOOS: "darwin", Getenv: func(string) string { return "" }}
	request := backend.OpenRequest{Project: projects.Project{ID: "alpha"}, Profile: config.DefaultProfile()}
	registry := backend.NewRegistry(environment, tmux)

	_, err := selectDashboardOpenBackend(context.Background(), registry, request, backend.Tmux, environment)
	if err == nil || !strings.Contains(err.Error(), "supports only cmux or Windows Terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

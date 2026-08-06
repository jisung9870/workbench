package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeProjects struct {
	project projects.Project
	found   bool
}

func (f fakeProjects) Show(string) (projects.Project, bool, error) { return f.project, f.found, nil }

type memoryHistory struct{ items []Result }

func (h *memoryHistory) List(projectID string) ([]Result, error) {
	result := []Result{}
	for _, item := range h.items {
		if projectID == "" || item.ProjectID == projectID {
			result = append(result, item)
		}
	}
	return result, nil
}
func (h *memoryHistory) Show(id string) (Result, bool, error) {
	for _, item := range h.items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return Result{}, false, nil
}
func (h *memoryHistory) Append(item Result) (string, error) {
	h.items = append([]Result{item}, h.items...)
	return "backup", nil
}

type fakeExecutor struct {
	paths     map[string]string
	command   Command
	limit     int
	execution Execution
	err       error
	wait      bool
}

type fakeLauncher struct {
	location                        LaunchLocation
	err                             error
	project, cwd, runID, executable string
}

func (l *fakeLauncher) Launch(_ context.Context, project, cwd, runID, executable string) (LaunchLocation, error) {
	l.project, l.cwd, l.runID, l.executable = project, cwd, runID, executable
	return l.location, l.err
}
func (l *fakeLauncher) VerifyWorker(context.Context, string, func(string) string) error { return l.err }

type tmuxExec struct {
	paths    map[string]string
	requests []backend.ProcessRequest
	results  []backend.ProcessResult
	errors   []error
}

func (e *tmuxExec) LookPath(name string) (string, error) {
	if value := e.paths[name]; value != "" {
		return value, nil
	}
	return "", errors.New("missing")
}
func (e *tmuxExec) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	e.requests = append(e.requests, request)
	i := len(e.requests) - 1
	var result backend.ProcessResult
	var err error
	if i < len(e.results) {
		result = e.results[i]
	}
	if i < len(e.errors) {
		err = e.errors[i]
	}
	return result, err
}

func (e *fakeExecutor) LookPath(name string) (string, error) {
	if path := e.paths[name]; path != "" {
		return path, nil
	}
	return "", errors.New("missing")
}
func (e *fakeExecutor) Run(ctx context.Context, command Command, limit int) (Execution, error) {
	e.command, e.limit = command, limit
	if e.wait {
		<-ctx.Done()
		return Execution{ExitCode: -1}, ctx.Err()
	}
	return e.execution, e.err
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectTestUsesCanonicalRegistryPathAndExactArgv(t *testing.T) {
	real := t.TempDir()
	canonicalReal, err := projects.CanonicalPath(real)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(real, "go.mod"), "module example\n")
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{paths: map[string]string{"go": "/tools/go"}, execution: Execution{ExitCode: 0, Output: "ok"}}
	history := &memoryHistory{}
	manager := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: link}, found: true}, history, executor, time.Now)
	result, _, err := manager.Run(context.Background(), ProjectTest, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	want := Command{Executable: "/tools/go", Args: []string{"test", "./..."}, Dir: canonicalReal}
	if !reflect.DeepEqual(executor.command, want) {
		t.Fatalf("command = %#v, want %#v", executor.command, want)
	}
	if executor.limit != OutputLimit || result.Status != Succeeded || len(history.items) != 1 {
		t.Fatalf("unexpected run: %#v limit=%d history=%d", result, executor.limit, len(history.items))
	}
}

func TestCatalogUsesOnlyAllowlistedTypedWorkflows(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.tf"), "terraform {}")
	manager := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, &memoryHistory{}, &fakeExecutor{paths: map[string]string{"bb": "/tools/bb", "trivy": "/tools/trivy", "terraform": "/tools/terraform"}}, time.Now)
	items, err := manager.Catalog(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("catalog len=%d", len(items))
	}
	if items[0].ID != ProjectTest || items[0].Status != "skipped" || items[1].ID != SecurityScan || items[1].Status != "available" || items[2].ID != TerraformPlan || items[2].Status != "available" {
		t.Fatalf("unexpected catalog: %#v", items)
	}
	_, _, err = manager.Run(context.Background(), "shell", "alpha")
	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("arbitrary workflow accepted: %v", err)
	}
}

func TestProvidersUseExactSafeArgumentArrays(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := projects.CanonicalPath(root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "main.tf"), "terraform {}")
	for _, test := range []struct {
		id   string
		args []string
	}{{SecurityScan, []string{"tvx", "ci", "repo", "."}}, {TerraformPlan, []string{"tfx", "plan", "-input=false", "-no-color"}}} {
		executor := &fakeExecutor{paths: map[string]string{"bb": "/tools/bb", "trivy": "/tools/trivy", "terraform": "/tools/terraform"}, execution: Execution{ExitCode: 0}}
		manager := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, &memoryHistory{}, executor, time.Now)
		if _, _, err := manager.Run(context.Background(), test.id, "alpha"); err != nil {
			t.Fatal(err)
		}
		if executor.command.Executable != "/tools/bb" || !reflect.DeepEqual(executor.command.Args, test.args) || executor.command.Dir != canonicalRoot {
			t.Fatalf("unsafe command for %s: %#v", test.id, executor.command)
		}
	}
}

func TestUnavailableProviderAndSkippedProjectAreExplicit(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, &memoryHistory{}, &fakeExecutor{}, time.Now)
	items, err := manager.Catalog(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != "skipped" || items[1].Status != "unavailable" || items[2].Status != "skipped" {
		t.Fatalf("unexpected availability: %#v", items)
	}
	_, _, err = manager.Run(context.Background(), SecurityScan, "alpha")
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing provider was not unavailable: %v", err)
	}
}

func TestTimeoutCancellationAndOutputCapAreRecorded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example\n")
	executor := &fakeExecutor{paths: map[string]string{"go": "/tools/go"}, wait: true}
	history := &memoryHistory{}
	manager := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, history, executor, time.Now)
	manager.timeout = func(Definition) time.Duration { return time.Millisecond }
	result, _, err := manager.Run(context.Background(), ProjectTest, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != TimedOut {
		t.Fatalf("status=%s", result.Status)
	}
	executor.wait = false
	executor.execution = Execution{ExitCode: 0, Output: string(make([]byte, OutputLimit+10))}
	manager.timeout = func(item Definition) time.Duration { return item.Timeout }
	result, _, err = manager.Run(context.Background(), ProjectTest, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != OutputLimit || !result.OutputTruncated {
		t.Fatalf("output cap not enforced: len=%d truncated=%v", len(result.Output), result.OutputTruncated)
	}
}

func TestStoreBoundsHistoryAndCreatesBackup(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root, WorkflowsFile: filepath.Join(root, "workflows.json"), BackupsDir: filepath.Join(root, "backups")}
	store := NewStore(paths)
	now := time.Now().UTC()
	lastBackup := ""
	for i := 0; i < historyLimit+3; i++ {
		code := 0
		backup, err := store.Append(Result{ID: string(rune('a' + i)), WorkflowID: ProjectTest, ProjectID: "alpha", Status: Succeeded, ExitCode: &code, StartedAt: now.Add(time.Duration(i) * time.Second), FinishedAt: now.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		lastBackup = backup
	}
	items, err := store.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != historyLimit {
		t.Fatalf("history len=%d", len(items))
	}
	if lastBackup == "" {
		t.Fatal("expected backup for existing history")
	}
	backups, err := filepath.Glob(filepath.Join(root, "backups", "workflows.json-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) > backupLimit {
		t.Fatalf("backup count=%d", len(backups))
	}
}

func TestStoreNeverPersistsCapturedOutput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflows.json")
	store := NewStore(config.Paths{StateDir: root, WorkflowsFile: path, BackupsDir: filepath.Join(root, "backups")})
	code := 0
	now := time.Now().UTC()
	sentinel := "SECRET_ENV_SENTINEL"
	if _, err := store.Append(Result{ID: "run-secret", WorkflowID: ProjectTest, ProjectID: "alpha", Status: Succeeded, ExitCode: &code, StartedAt: now, FinishedAt: now, Output: sentinel, OutputTruncated: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) || strings.Contains(string(data), `"output"`) {
		t.Fatalf("raw output persisted: %s", data)
	}
	loaded, found, err := store.Show("run-secret")
	if err != nil || !found || loaded.Output != "" {
		t.Fatalf("loaded output=%q found=%v err=%v", loaded.Output, found, err)
	}
}

func TestOSExecutorCancellationTerminatesChildProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group assertion")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, runErr := (&OSExecutor{}).Run(ctx, Command{Executable: sh, Args: []string{"-c", `(sleep 0.3; printf survived > "$1") & wait`, "sh", marker}, Dir: t.TempDir()}, OutputLimit)
	if runErr == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expected deadline failure: result=%#v err=%v", result, runErr)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child survived process-tree cancellation: %v", err)
	}
}

func TestResultJSONDoesNotPersistCommandOrEnvironment(t *testing.T) {
	code := 0
	now := time.Now().UTC()
	encoded, err := json.Marshal(Result{ID: "run-1", WorkflowID: ProjectTest, ProjectID: "alpha", Status: Succeeded, ExitCode: &code, StartedAt: now, FinishedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"command", "executable", "argv", "environment", "secret"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("result persisted forbidden field %s: %s", strconv.Quote(forbidden), text)
		}
	}
}

func TestLaunchPersistsBeforeDetachedTmuxAndReturnsRunning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example\n")
	paths := config.Paths{StateDir: root, WorkflowsFile: filepath.Join(root, "state", "workflows.json"), BackupsDir: filepath.Join(root, "backups")}
	store := NewStore(paths)
	launcher := &fakeLauncher{location: LaunchLocation{PaneID: "%7", SessionName: "alpha"}}
	m := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, store, &fakeExecutor{paths: map[string]string{"go": "/go"}}, time.Now)
	m.launcher = launcher
	m.workerExecutable = func() (string, error) { return "/opt/wb", nil }
	ctx, cancel := context.WithCancel(context.Background())
	result, _, err := m.Launch(ctx, ProjectTest, "alpha")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Starting || result.PaneID != "%7" || launcher.executable != "/opt/wb" {
		t.Fatalf("unexpected detached launch: %#v launcher=%#v", result, launcher)
	}
	stored, found, err := store.Show(result.ID)
	if err != nil || !found || stored.Status != Starting {
		t.Fatalf("pending/running record missing: %#v %v", stored, err)
	}
}

func TestWorkerClaimsOnceAndCompletesStoredAllowlist(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example\n")
	paths := config.Paths{StateDir: root, WorkflowsFile: filepath.Join(root, "workflows.json"), BackupsDir: filepath.Join(root, "backups")}
	store := NewStore(paths)
	now := time.Now().UTC()
	pending := Result{ID: "run-100-abcdef12", WorkflowID: ProjectTest, ProjectID: "alpha", Status: Pending, StartedAt: now, FinishedAt: now}
	if _, err := store.Create(pending); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkLaunched(pending.ID, LaunchLocation{PaneID: "%1", SessionName: "alpha"}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, store, &fakeExecutor{paths: map[string]string{"go": "/go"}, execution: Execution{ExitCode: 0, Output: "ok"}}, time.Now)
	m.launcher = &fakeLauncher{}
	result, _, err := m.Worker(context.Background(), pending.ID)
	if err != nil || result.Status != Succeeded {
		t.Fatalf("worker failed: %#v %v", result, err)
	}
	_, _, err = m.Worker(context.Background(), pending.ID)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("replay was accepted: %v", err)
	}
}

func TestWorkerTimeoutCompletesTerminalRecord(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example\n")
	paths := config.Paths{StateDir: root, WorkflowsFile: filepath.Join(root, "workflows.json"), BackupsDir: filepath.Join(root, "backups")}
	store := NewStore(paths)
	now := time.Now().UTC()
	pending := Result{ID: "run-101-abcdef12", WorkflowID: ProjectTest, ProjectID: "alpha", Status: Pending, StartedAt: now, FinishedAt: now}
	if _, err := store.Create(pending); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkLaunched(pending.ID, LaunchLocation{PaneID: "%1", SessionName: "alpha"}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, store, &fakeExecutor{paths: map[string]string{"go": "/go"}, wait: true}, time.Now)
	m.launcher = &fakeLauncher{}
	m.timeout = func(Definition) time.Duration { return time.Millisecond }
	result, _, err := m.Worker(context.Background(), pending.ID)
	if err != nil || result.Status != TimedOut {
		t.Fatalf("timeout result=%#v err=%v", result, err)
	}
}

func TestWorkerWaitsForStartingBeforeClaim(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example\n")
	store := NewStore(config.Paths{StateDir: root, WorkflowsFile: filepath.Join(root, "workflows.json"), BackupsDir: filepath.Join(root, "backups")})
	now := time.Now().UTC()
	pending := Result{ID: "run-102-abcdef12", WorkflowID: ProjectTest, ProjectID: "alpha", Status: Pending, StartedAt: now, FinishedAt: now}
	if _, err := store.Create(pending); err != nil {
		t.Fatal(err)
	}
	m := NewManager(fakeProjects{project: projects.Project{ID: "alpha", Path: root}, found: true}, store, &fakeExecutor{paths: map[string]string{"go": "/go"}, execution: Execution{ExitCode: 0}}, time.Now)
	m.launcher = &fakeLauncher{}
	done := make(chan error, 1)
	go func() { _, _, err := m.Worker(context.Background(), pending.ID); done <- err }()
	time.Sleep(25 * time.Millisecond)
	starting, _, err := store.MarkLaunched(pending.ID, LaunchLocation{PaneID: "%2", SessionName: "alpha"})
	if err != nil || starting.Status != Starting {
		t.Fatalf("mark launched=%#v err=%v", starting, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	terminal, _, err := store.Show(pending.ID)
	if err != nil || terminal.Status != Succeeded {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
}

func TestTmuxLauncherUsesExactArgumentArrayAndWorkerIDOnly(t *testing.T) {
	exec := &tmuxExec{paths: map[string]string{"tmux": "/tmux"}, results: []backend.ProcessResult{{}, {Stdout: "%9\n"}, {}, {}}}
	launcher := NewTmuxLauncher(exec)
	location, err := launcher.Launch(context.Background(), "alpha", "/repo with spaces", "run-100-abcdef12", "/opt/wb app")
	if err != nil {
		t.Fatal(err)
	}
	if location.PaneID != "%9" {
		t.Fatalf("location=%#v", location)
	}
	want := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=alpha", "-n", "wf-abcdef12", "-c", "/repo with spaces", "exec '/opt/wb app' 'workflows' 'worker' 'run-100-abcdef12'"}
	if !reflect.DeepEqual(exec.requests[1].Args, want) {
		t.Fatalf("tmux argv=%#v", exec.requests[1].Args)
	}
}

func TestDetachedLaunchReportsUnavailableTmux(t *testing.T) {
	launcher := NewTmuxLauncher(&tmuxExec{})
	_, err := launcher.Launch(context.Background(), "alpha", "/repo", "run-100-abcdef12", "/wb")
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing tmux not unavailable: %v", err)
	}
}

package workflows

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

const (
	ProjectTest   = "project.test"
	SecurityScan  = "security.scan"
	TerraformPlan = "terraform.plan"
	OutputLimit   = 64 << 10
)

type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	TimedOut  Status = "timed_out"
	Cancelled Status = "cancelled"
	Pending   Status = "pending"
	Running   Status = "running"
)

type Definition struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Timeout     time.Duration `json:"-"`
	TimeoutSecs int           `json:"timeout_seconds"`
	Risk        string        `json:"risk"`
}

type Availability struct {
	Definition
	ProjectID string `json:"project_id,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type Command struct {
	Executable string
	Args       []string
	Dir        string
}

type Execution struct {
	ExitCode        int
	Output          string
	OutputTruncated bool
}

type Executor interface {
	LookPath(string) (string, error)
	Run(context.Context, Command, int) (Execution, error)
}

type ProjectStore interface {
	Show(string) (projects.Project, bool, error)
}

type History interface {
	List(string) ([]Result, error)
	Show(string) (Result, bool, error)
	Append(Result) (string, error)
}

type Result struct {
	ID              string    `json:"id"`
	WorkflowID      string    `json:"workflow_id"`
	ProjectID       string    `json:"project_id"`
	Status          Status    `json:"status"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	DurationMillis  int64     `json:"duration_millis"`
	Output          string    `json:"-"`
	OutputTruncated bool      `json:"output_truncated"`
	PaneID          string    `json:"pane_id,omitempty"`
	SessionName     string    `json:"session_name,omitempty"`
}

type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

type NotFoundError struct{ Message string }

func (e *NotFoundError) Error() string { return e.Message }

type UnavailableError struct{ Message string }

func (e *UnavailableError) Error() string { return e.Message }

type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

type PartialError struct {
	Result Result
	Backup string
	Cause  error
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("workflow %s finished with status %s but its result could not be persisted: %v", e.Result.WorkflowID, e.Result.Status, e.Cause)
}
func (e *PartialError) Unwrap() error { return e.Cause }

type Manager struct {
	projects         ProjectStore
	history          History
	executor         Executor
	now              func() time.Time
	timeout          func(Definition) time.Duration
	launcher         DetachedLauncher
	workerExecutable func() (string, error)
}

func New(paths config.Paths) *Manager {
	processExecutor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
	return &Manager{projects: projects.NewStore(paths), history: NewStore(paths), executor: &OSExecutor{Live: os.Stdout}, now: time.Now, timeout: func(item Definition) time.Duration { return item.Timeout }, launcher: NewTmuxLauncher(processExecutor), workerExecutable: os.Executable}
}

type LaunchLocation struct{ PaneID, SessionName string }
type DetachedLauncher interface {
	Launch(context.Context, string, string, string, string) (LaunchLocation, error)
}

func (m *Manager) Launch(ctx context.Context, workflowID, projectID string) (Result, string, error) {
	item, ok := definition(workflowID)
	if !ok {
		return Result{}, "", &InvalidError{Message: fmt.Sprintf("workflow %q is not allowlisted", workflowID)}
	}
	project, err := m.project(projectID)
	if err != nil {
		return Result{}, "", err
	}
	availability, _ := m.resolve(project, item)
	if availability.Status != "available" {
		return Result{}, "", &UnavailableError{Message: fmt.Sprintf("workflow %s is %s for project %s: %s", workflowID, availability.Status, projectID, availability.Reason)}
	}
	if m.launcher == nil || m.workerExecutable == nil {
		return Result{}, "", &UnavailableError{Message: "detached tmux workflow launcher is unavailable"}
	}
	id, err := newID(m.now())
	if err != nil {
		return Result{}, "", err
	}
	now := m.now().UTC()
	pending := Result{ID: id, WorkflowID: workflowID, ProjectID: project.ID, Status: Pending, StartedAt: now, FinishedAt: now}
	store, ok := m.history.(interface {
		Create(Result) (string, error)
		MarkLaunched(string, LaunchLocation) (Result, string, error)
		FailLaunch(string, string) (Result, string, error)
	})
	if !ok {
		return Result{}, "", errors.New("workflow history does not support detached launch")
	}
	backup, err := store.Create(pending)
	if err != nil {
		return Result{}, backup, err
	}
	executable, err := m.workerExecutable()
	if err != nil {
		_, _, _ = store.FailLaunch(id, err.Error())
		return pending, backup, err
	}
	location, launchErr := m.launcher.Launch(ctx, project.ID, project.Path, id, executable)
	if launchErr != nil {
		failed, failedBackup, persistErr := store.FailLaunch(id, launchErr.Error())
		if persistErr != nil {
			return failed, failedBackup, &PartialError{Result: failed, Backup: failedBackup, Cause: persistErr}
		}
		return failed, failedBackup, &UnavailableError{Message: fmt.Sprintf("launch workflow in tmux: %v", launchErr)}
	}
	running, updateBackup, err := store.MarkLaunched(id, location)
	if err != nil {
		return pending, updateBackup, &PartialError{Result: pending, Backup: updateBackup, Cause: err}
	}
	return running, updateBackup, nil
}

func (m *Manager) Worker(ctx context.Context, runID string) (Result, string, error) {
	if !runIDPattern.MatchString(runID) {
		return Result{}, "", &InvalidError{Message: "invalid workflow run ID"}
	}
	verifier, verified := m.launcher.(interface {
		VerifyWorker(context.Context, string, func(string) string) error
	})
	if !verified {
		return Result{}, "", &UnavailableError{Message: "workflow pane ownership verifier is unavailable"}
	}
	if err := verifier.VerifyWorker(ctx, runID, os.Getenv); err != nil {
		return Result{}, "", err
	}
	store, ok := m.history.(interface {
		Claim(string) (Result, string, error)
		Complete(string, Result) (Result, string, error)
	})
	if !ok {
		return Result{}, "", errors.New("workflow history does not support workers")
	}
	run, backup, err := store.Claim(runID)
	if err != nil {
		return Result{}, backup, err
	}
	item, allowed := definition(run.WorkflowID)
	if !allowed {
		return Result{}, backup, &InvalidError{Message: "stored workflow is not allowlisted"}
	}
	project, err := m.project(run.ProjectID)
	if err != nil {
		return m.completeWorker(store, run, Execution{ExitCode: -1, Output: err.Error()}, err, backup)
	}
	availability, command := m.resolve(project, item)
	if availability.Status != "available" {
		unavailable := errors.New(availability.Reason)
		return m.completeWorker(store, run, Execution{ExitCode: -1, Output: availability.Reason}, unavailable, backup)
	}
	runCtx, cancel := context.WithTimeout(ctx, m.timeout(item))
	defer cancel()
	execution, runErr := m.executor.Run(runCtx, command, OutputLimit)
	execution.Output, execution.OutputTruncated = capOutput(execution.Output, execution.OutputTruncated)
	return m.completeWorker(store, run, execution, errors.Join(runErr, runCtx.Err()), backup)
}

func (m *Manager) completeWorker(store interface {
	Complete(string, Result) (Result, string, error)
}, run Result, execution Execution, runErr error, priorBackup string) (Result, string, error) {
	finished := m.now().UTC()
	status := Succeeded
	if errors.Is(runErr, context.DeadlineExceeded) {
		status = TimedOut
	} else if errors.Is(runErr, context.Canceled) {
		status = Cancelled
	} else if runErr != nil || execution.ExitCode != 0 {
		status = Failed
	}
	completion := Result{Status: status, FinishedAt: finished, DurationMillis: finished.Sub(run.StartedAt).Milliseconds(), Output: execution.Output, OutputTruncated: execution.OutputTruncated}
	if execution.ExitCode >= 0 {
		code := execution.ExitCode
		completion.ExitCode = &code
	}
	updated, backup, err := store.Complete(run.ID, completion)
	if backup == "" {
		backup = priorBackup
	}
	if err != nil {
		return updated, backup, &PartialError{Result: updated, Backup: backup, Cause: err}
	}
	return updated, backup, nil
}

var runIDPattern = regexp.MustCompile(`^run-[0-9]+-[0-9a-f]{8}$`)

func NewManager(projectStore ProjectStore, history History, executor Executor, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{projects: projectStore, history: history, executor: executor, now: now, timeout: func(item Definition) time.Duration { return item.Timeout }}
}

func Definitions() []Definition {
	return []Definition{
		{ID: ProjectTest, Name: "Project tests", Description: "Run the repository-declared test entrypoint", Timeout: 5 * time.Minute, TimeoutSecs: 300, Risk: "executes-project-code"},
		{ID: SecurityScan, Name: "Security scan", Description: "Scan the registered repository with binbox tvx", Timeout: 10 * time.Minute, TimeoutSecs: 600, Risk: "repository-scan"},
		{ID: TerraformPlan, Name: "Terraform plan", Description: "Create a Terraform execution plan without applying it", Timeout: 10 * time.Minute, TimeoutSecs: 600, Risk: "writes-plan-file"},
	}
}

func definition(id string) (Definition, bool) {
	for _, item := range Definitions() {
		if item.ID == id {
			return item, true
		}
	}
	return Definition{}, false
}

func (m *Manager) Catalog(ctx context.Context, projectID string) ([]Availability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if projectID == "" {
		items := make([]Availability, 0, len(Definitions()))
		for _, item := range Definitions() {
			items = append(items, Availability{Definition: item, Status: "requires_project"})
		}
		return items, nil
	}
	project, err := m.project(projectID)
	if err != nil {
		return nil, err
	}
	items := make([]Availability, 0, len(Definitions()))
	for _, item := range Definitions() {
		availability, _ := m.resolve(project, item)
		items = append(items, availability)
	}
	return items, nil
}

func (m *Manager) Run(ctx context.Context, workflowID, projectID string) (Result, string, error) {
	item, ok := definition(workflowID)
	if !ok {
		return Result{}, "", &InvalidError{Message: fmt.Sprintf("workflow %q is not allowlisted", workflowID)}
	}
	project, err := m.project(projectID)
	if err != nil {
		return Result{}, "", err
	}
	availability, command := m.resolve(project, item)
	if availability.Status != "available" {
		return Result{}, "", &UnavailableError{Message: fmt.Sprintf("workflow %s is %s for project %s: %s", workflowID, availability.Status, projectID, availability.Reason)}
	}
	id, err := newID(m.now())
	if err != nil {
		return Result{}, "", err
	}
	started := m.now().UTC()
	runCtx, cancel := context.WithTimeout(ctx, m.timeout(item))
	defer cancel()
	execution, runErr := m.executor.Run(runCtx, command, OutputLimit)
	execution.Output, execution.OutputTruncated = capOutput(execution.Output, execution.OutputTruncated)
	finished := m.now().UTC()
	status := Succeeded
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		status = TimedOut
	} else if errors.Is(runCtx.Err(), context.Canceled) {
		status = Cancelled
	} else if runErr != nil || execution.ExitCode != 0 {
		status = Failed
	}
	result := Result{ID: id, WorkflowID: workflowID, ProjectID: project.ID, Status: status, StartedAt: started, FinishedAt: finished, DurationMillis: finished.Sub(started).Milliseconds(), Output: execution.Output, OutputTruncated: execution.OutputTruncated}
	if execution.ExitCode >= 0 {
		code := execution.ExitCode
		result.ExitCode = &code
	}
	backup, persistErr := m.history.Append(result)
	if persistErr != nil {
		return result, backup, &PartialError{Result: result, Backup: backup, Cause: persistErr}
	}
	return result, backup, nil
}

func (m *Manager) History(projectID string) ([]Result, error) { return m.history.List(projectID) }
func (m *Manager) Show(id string) (Result, bool, error)       { return m.history.Show(id) }
func (m *Manager) Jump(ctx context.Context, id string, allowAttach bool, getenv func(string) string) error {
	run, found, err := m.history.Show(id)
	if err != nil {
		return err
	}
	if !found || run.PaneID == "" || (run.Status != Pending && run.Status != Running) {
		return &UnavailableError{Message: "workflow task is not active in tmux"}
	}
	jumper, ok := m.launcher.(interface {
		Jump(context.Context, Result, bool, func(string) string) error
	})
	if !ok {
		return &UnavailableError{Message: "workflow tmux jump is unavailable"}
	}
	return jumper.Jump(ctx, run, allowAttach, getenv)
}

func (m *Manager) project(id string) (projects.Project, error) {
	if strings.TrimSpace(id) == "" {
		return projects.Project{}, &InvalidError{Message: "project ID is required"}
	}
	project, found, err := m.projects.Show(id)
	if err != nil {
		return projects.Project{}, err
	}
	if !found {
		return projects.Project{}, &NotFoundError{Message: fmt.Sprintf("project %q was not found", id)}
	}
	canonical, err := projects.CanonicalPath(project.Path)
	if err != nil {
		return projects.Project{}, &UnavailableError{Message: fmt.Sprintf("project %q path is unavailable: %v", id, err)}
	}
	project.Path, project.RepoRoot = canonical, canonical
	return project, nil
}

func (m *Manager) resolve(project projects.Project, item Definition) (Availability, Command) {
	result := Availability{Definition: item, ProjectID: project.ID, Status: "available"}
	switch item.ID {
	case ProjectTest:
		if fileExists(filepath.Join(project.Path, "tests", "contract-test.sh")) {
			path, err := m.executor.LookPath("bash")
			if err != nil {
				result.Status, result.Reason = "unavailable", "bash executable was not found"
				return result, Command{}
			}
			return result, Command{Executable: path, Args: []string{"tests/contract-test.sh", "--root-only"}, Dir: project.Path}
		}
		if fileExists(filepath.Join(project.Path, "go.mod")) {
			path, err := m.executor.LookPath("go")
			if err != nil {
				result.Status, result.Reason = "unavailable", "go executable was not found"
				return result, Command{}
			}
			return result, Command{Executable: path, Args: []string{"test", "./..."}, Dir: project.Path}
		}
		result.Status, result.Reason = "skipped", "no supported repository test entrypoint was detected"
	case SecurityScan:
		path, err := m.executor.LookPath("bb")
		if err != nil {
			result.Status, result.Reason = "unavailable", "binbox executable bb was not found"
			return result, Command{}
		}
		if _, err := m.executor.LookPath("trivy"); err != nil {
			result.Status, result.Reason = "unavailable", "trivy executable was not found"
			return result, Command{}
		}
		return result, Command{Executable: path, Args: []string{"tvx", "ci", "repo", "."}, Dir: project.Path}
	case TerraformPlan:
		matches, _ := filepath.Glob(filepath.Join(project.Path, "*.tf"))
		hasTerraform := false
		for _, match := range matches {
			if fileExists(match) {
				hasTerraform = true
				break
			}
		}
		if !hasTerraform {
			result.Status, result.Reason = "skipped", "no root Terraform configuration was detected"
			return result, Command{}
		}
		path, err := m.executor.LookPath("bb")
		if err != nil {
			result.Status, result.Reason = "unavailable", "binbox executable bb was not found"
			return result, Command{}
		}
		if _, err := m.executor.LookPath("terraform"); err != nil {
			result.Status, result.Reason = "unavailable", "terraform executable was not found"
			return result, Command{}
		}
		return result, Command{Executable: path, Args: []string{"tfx", "plan", "-input=false", "-no-color"}, Dir: project.Path}
	default:
		result.Status, result.Reason = "unavailable", "workflow is not allowlisted"
	}
	return result, Command{}
}

func fileExists(path string) bool { info, err := os.Stat(path); return err == nil && !info.IsDir() }

func capOutput(output string, alreadyTruncated bool) (string, bool) {
	if len(output) <= OutputLimit {
		return output, alreadyTruncated
	}
	return output[:OutputLimit], true
}

func newID(now time.Time) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("run-%d-%s", now.UTC().UnixMilli(), hex.EncodeToString(random)), nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
	mu        sync.Mutex
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(p)
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buffer.Write(p[:remaining])
		} else {
			_, _ = w.buffer.Write(p)
		}
	}
	if original > remaining {
		w.truncated = true
	}
	return original, nil
}

type OSExecutor struct{ Live io.Writer }

func (*OSExecutor) LookPath(name string) (string, error) { return exec.LookPath(name) }
func (executor *OSExecutor) Run(ctx context.Context, command Command, outputLimit int) (Execution, error) {
	process := exec.CommandContext(ctx, command.Executable, command.Args...)
	process.Dir = command.Dir
	prepareWorkflowCommand(process)
	process.Cancel = func() error { return terminateWorkflowProcessTree(process.Process) }
	buffer := &limitedBuffer{limit: outputLimit}
	writers := []io.Writer{buffer}
	if executor.Live != nil {
		writers = append(writers, executor.Live)
	}
	live := io.MultiWriter(writers...)
	process.Stdout, process.Stderr = live, live
	err := process.Run()
	result := Execution{ExitCode: -1, Output: buffer.buffer.String(), OutputTruncated: buffer.truncated}
	if process.ProcessState != nil {
		result.ExitCode = process.ProcessState.ExitCode()
	}
	return result, err
}

var _ io.Writer = (*limitedBuffer)(nil)

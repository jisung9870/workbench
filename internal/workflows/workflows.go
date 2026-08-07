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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/secrets"
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
	Starting  Status = "starting"
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
	Executable   string
	Args         []string
	Dir          string
	Environment  map[string]string `json:"-"`
	SecretValues [][]byte          `json:"-"`
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

type EnvironmentStore interface {
	Show(string) (environments.Environment, bool, error)
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
	EnvironmentID   string    `json:"environment_id,omitempty"`
	ResolveSecrets  bool      `json:"resolve_secrets"`
}

type RunOptions struct {
	EnvironmentID  string
	NoEnvironment  bool
	ResolveSecrets bool
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
	environments     EnvironmentStore
	secrets          environments.SecretGetter
	history          History
	executor         Executor
	now              func() time.Time
	timeout          func(Definition) time.Duration
	launcher         DetachedLauncher
	workerExecutable func() (string, error)
}

func New(paths config.Paths) *Manager {
	processExecutor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
	return &Manager{projects: projects.NewStore(paths), environments: environments.NewStore(paths), secrets: secrets.NewStore(paths), history: NewStore(paths), executor: &OSExecutor{Live: os.Stdout}, now: time.Now, timeout: func(item Definition) time.Duration { return item.Timeout }, launcher: NewTmuxLauncher(processExecutor), workerExecutable: os.Executable}
}

type LaunchLocation struct{ PaneID, SessionName string }
type DetachedLauncher interface {
	Launch(context.Context, string, string, string, string) (LaunchLocation, error)
}

func (m *Manager) Launch(ctx context.Context, workflowID, projectID string) (Result, string, error) {
	return m.LaunchWithOptions(ctx, workflowID, projectID, RunOptions{})
}

func (m *Manager) LaunchWithOptions(ctx context.Context, workflowID, projectID string, options RunOptions) (Result, string, error) {
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
	environmentID, err := m.preflightEnvironment(project, options)
	if err != nil {
		return Result{}, "", err
	}
	pending := Result{ID: id, WorkflowID: workflowID, ProjectID: project.ID, EnvironmentID: environmentID, ResolveSecrets: options.ResolveSecrets, Status: Pending, StartedAt: now, FinishedAt: now}
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
	if err := m.injectEnvironment(&command, run.EnvironmentID, run.ResolveSecrets); err != nil {
		return m.completeWorker(store, run, Execution{ExitCode: -1, Output: "environment preparation failed"}, err, backup)
	}
	defer zeroCommandSecrets(&command)
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

func (m *Manager) WithEnvironmentStores(environmentStore EnvironmentStore, secretStore environments.SecretGetter) *Manager {
	m.environments, m.secrets = environmentStore, secretStore
	return m
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
	return m.RunWithOptions(ctx, workflowID, projectID, RunOptions{})
}

func (m *Manager) RunWithOptions(ctx context.Context, workflowID, projectID string, options RunOptions) (Result, string, error) {
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
	environmentID, err := m.preflightEnvironment(project, options)
	if err != nil {
		return Result{}, "", err
	}
	if err := m.injectEnvironment(&command, environmentID, options.ResolveSecrets); err != nil {
		return Result{}, "", err
	}
	defer zeroCommandSecrets(&command)
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
	result := Result{ID: id, WorkflowID: workflowID, ProjectID: project.ID, EnvironmentID: environmentID, ResolveSecrets: options.ResolveSecrets, Status: status, StartedAt: started, FinishedAt: finished, DurationMillis: finished.Sub(started).Milliseconds(), Output: execution.Output, OutputTruncated: execution.OutputTruncated}
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
	if !found || run.PaneID == "" || (run.Status != Pending && run.Status != Starting && run.Status != Running) {
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

func (m *Manager) preflightEnvironment(project projects.Project, options RunOptions) (string, error) {
	if options.NoEnvironment && options.EnvironmentID != "" {
		return "", &InvalidError{Message: "--environment and --no-environment cannot be combined"}
	}
	environmentID := project.EnvironmentID
	if options.NoEnvironment {
		environmentID = ""
	} else if options.EnvironmentID != "" {
		environmentID = options.EnvironmentID
	}
	if options.ResolveSecrets && environmentID == "" {
		return "", &InvalidError{Message: "--resolve-secrets requires an environment"}
	}
	if environmentID == "" {
		return "", nil
	}
	if m.environments == nil {
		return "", &UnavailableError{Message: "environment registry is unavailable"}
	}
	environment, found, err := m.environments.Show(environmentID)
	if err != nil {
		return "", &UnavailableError{Message: "environment registry is unavailable"}
	}
	if !found {
		return "", &NotFoundError{Message: fmt.Sprintf("environment %q was not found", environmentID)}
	}
	if expiry := environments.ExpiryAt(environment, m.now()); expiry.Status == environments.ExpiryExpired {
		return "", &UnavailableError{Message: fmt.Sprintf("environment %q is expired", environmentID)}
	}
	for key, value := range environments.ExportValues(environment) {
		if strings.IndexByte(value, 0) >= 0 {
			return "", &InvalidError{Message: fmt.Sprintf("environment variable %q cannot be injected", key)}
		}
	}
	if options.ResolveSecrets {
		if m.secrets == nil {
			return "", &UnavailableError{Message: "secrets store is unavailable"}
		}
		resolved, _, resolveErr := environments.ResolveSecretReferences(environment, m.secrets)
		if resolveErr == nil {
			for key, value := range resolved {
				if bytes.IndexByte(value, 0) >= 0 {
					resolveErr = &InvalidError{Message: fmt.Sprintf("secret variable %q cannot be injected", key)}
					break
				}
			}
		}
		environments.ZeroResolvedSecrets(resolved)
		if resolveErr != nil {
			return "", &UnavailableError{Message: "one or more secret references are unavailable"}
		}
	}
	return environmentID, nil
}

func (m *Manager) injectEnvironment(command *Command, environmentID string, resolveSecrets bool) error {
	if environmentID == "" {
		return nil
	}
	if m.environments == nil {
		return &UnavailableError{Message: "environment registry is unavailable"}
	}
	environment, found, err := m.environments.Show(environmentID)
	if err != nil || !found {
		return &UnavailableError{Message: "selected environment is unavailable"}
	}
	if expiry := environments.ExpiryAt(environment, m.now()); expiry.Status == environments.ExpiryExpired {
		return &UnavailableError{Message: fmt.Sprintf("environment %q is expired", environmentID)}
	}
	values := environments.ExportValues(environment)
	for key, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return &InvalidError{Message: fmt.Sprintf("environment variable %q cannot be injected", key)}
		}
	}
	command.Environment = values
	if !resolveSecrets {
		return nil
	}
	if m.secrets == nil {
		return &UnavailableError{Message: "secrets store is unavailable"}
	}
	resolved, _, err := environments.ResolveSecretReferences(environment, m.secrets)
	if err != nil {
		return &UnavailableError{Message: "one or more secret references are unavailable"}
	}
	for key, value := range resolved {
		if bytes.IndexByte(value, 0) >= 0 {
			environments.ZeroResolvedSecrets(resolved)
			return &InvalidError{Message: fmt.Sprintf("secret variable %q cannot be injected", key)}
		}
		command.Environment[key] = string(value)
		command.SecretValues = append(command.SecretValues, value)
	}
	return nil
}

func zeroCommandSecrets(command *Command) {
	for _, value := range command.SecretValues {
		for i := range value {
			value[i] = 0
		}
	}
	command.SecretValues = nil
	for key := range command.Environment {
		delete(command.Environment, key)
	}
	command.Environment = nil
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
	merged, err := mergeEnvironment(os.Environ(), command.Environment, runtime.GOOS == "windows")
	if err != nil {
		return Execution{ExitCode: -1}, err
	}
	process.Env = merged
	prepareWorkflowCommand(process)
	process.Cancel = func() error { return terminateWorkflowProcessTree(process.Process) }
	captureLimit := outputLimit
	if len(command.SecretValues) > 0 {
		longest := 0
		for _, value := range command.SecretValues {
			if len(value) > longest {
				longest = len(value)
			}
		}
		captureLimit += longest
	}
	buffer := &limitedBuffer{limit: captureLimit}
	writers := []io.Writer{buffer}
	if executor.Live != nil && len(command.SecretValues) == 0 {
		writers = append(writers, executor.Live)
	}
	live := io.MultiWriter(writers...)
	process.Stdout, process.Stderr = live, live
	err = process.Run()
	output := buffer.buffer.String()
	if len(command.SecretValues) > 0 {
		output = redactSecrets(output, command.SecretValues)
		output, buffer.truncated = capOutput(output, buffer.truncated)
		if executor.Live != nil {
			written, writeErr := io.WriteString(executor.Live, output)
			if writeErr == nil && written != len(output) {
				writeErr = io.ErrShortWrite
			}
			if writeErr != nil {
				err = errors.Join(err, fmt.Errorf("write redacted workflow output: %w", writeErr))
			}
		}
	}
	result := Execution{ExitCode: -1, Output: output, OutputTruncated: buffer.truncated}
	if process.ProcessState != nil {
		result.ExitCode = process.ProcessState.ExitCode()
	}
	return result, err
}

func mergeEnvironment(inherited []string, injected map[string]string, windows bool) ([]string, error) {
	result := make([]string, 0, len(inherited)+len(injected))
	positions := map[string]int{}
	normalize := func(key string) string {
		if windows {
			return strings.ToUpper(key)
		}
		return key
	}
	add := func(key, value string, validateName bool) error {
		if key == "" || validateName && !environments.ValidVariableName(key) || strings.IndexByte(key, 0) >= 0 || strings.IndexByte(key, '=') >= 0 || strings.IndexByte(value, 0) >= 0 {
			return errors.New("invalid subprocess environment")
		}
		entry := key + "=" + value
		normalized := normalize(key)
		if index, exists := positions[normalized]; exists {
			result[index] = entry
		} else {
			positions[normalized] = len(result)
			result = append(result, entry)
		}
		return nil
	}
	for _, entry := range inherited {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		if err := add(key, value, false); err != nil {
			return nil, err
		}
	}
	for key, value := range injected {
		if err := add(key, value, true); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func redactSecrets(output string, values [][]byte) string {
	ordered := append([][]byte(nil), values...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, value := range ordered {
		if len(value) > 0 {
			output = strings.ReplaceAll(output, string(value), "[REDACTED]")
		}
	}
	return output
}

var _ io.Writer = (*limitedBuffer)(nil)

package agents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/worktrees"
)

type ProjectStore interface {
	Show(string) (projects.Project, bool, error)
}

type WorktreeResolver interface {
	Resolve(context.Context, string, string) (worktrees.Item, error)
}

type InvalidError struct{ Message string }

func (err *InvalidError) Error() string { return err.Message }

type NotFoundError struct{ Message string }

func (err *NotFoundError) Error() string { return err.Message }

type ConflictError struct{ Message string }

func (err *ConflictError) Error() string { return err.Message }

type PartialError struct {
	Message string
	Task    Task
	Backups []string
	Cause   error
}

func (err *PartialError) Error() string { return fmt.Sprintf("%s: %v", err.Message, err.Cause) }
func (err *PartialError) Unwrap() error { return err.Cause }

type StartRequest struct {
	ProjectID  string
	WorktreeID string
	AgentKind  string
	Backend    backend.Name
}

type Manager struct {
	paths     config.Paths
	projects  ProjectStore
	worktrees WorktreeResolver
	state     *StateStore
	executor  backend.Executor
	env       backend.Environment
	runtimes  map[backend.Name]Runtime
	now       func() time.Time
	newID     func(time.Time) (string, error)
}

func NewManager(paths config.Paths, projectStore ProjectStore, worktreeResolver WorktreeResolver, state *StateStore, executor backend.Executor, environment backend.Environment, runtimes ...Runtime) *Manager {
	registered := make(map[backend.Name]Runtime, len(runtimes))
	for _, agentRuntime := range runtimes {
		registered[agentRuntime.Name()] = agentRuntime
	}
	return &Manager{
		paths: paths, projects: projectStore, worktrees: worktreeResolver, state: state,
		executor: executor, env: environment, runtimes: registered, now: time.Now, newID: NewTaskID,
	}
}

func (manager *Manager) Start(ctx context.Context, request StartRequest) (Task, []string, error) {
	if request.AgentKind != "codex" && request.AgentKind != "claude" {
		return Task{}, nil, &InvalidError{Message: fmt.Sprintf("unsupported agent %q (expected codex or claude)", request.AgentKind)}
	}
	project, found, err := manager.projects.Show(request.ProjectID)
	if err != nil {
		return Task{}, nil, err
	}
	if !found {
		return Task{}, nil, &NotFoundError{Message: fmt.Sprintf("project %q was not found", request.ProjectID)}
	}
	cwd, err := projects.CanonicalPath(project.Path)
	if err != nil {
		return Task{}, nil, err
	}
	if request.WorktreeID != "" {
		item, resolveErr := manager.worktrees.Resolve(ctx, project.ID, request.WorktreeID)
		if resolveErr != nil {
			return Task{}, nil, resolveErr
		}
		cwd = item.Path
	}
	executable, err := manager.executor.LookPath(request.AgentKind)
	if err != nil {
		return Task{}, nil, &backend.UnavailableError{Backend: request.Backend, Reason: fmt.Sprintf("%s executable was not found", request.AgentKind)}
	}
	settings, err := config.LoadSettings(manager.paths.ConfigFile)
	if err != nil {
		return Task{}, nil, err
	}
	profile, err := config.LoadProfile(manager.paths, settings.ActiveProfile)
	if err != nil {
		return Task{}, nil, err
	}
	adapters := make([]backend.Adapter, 0, len(manager.runtimes))
	for _, agentRuntime := range manager.runtimes {
		adapters = append(adapters, selectableRuntime{agentRuntime})
	}
	registry := backend.NewRegistry(manager.env, adapters...)
	selection, err := registry.Select(ctx, backend.OpenRequest{Project: project, Profile: profile}, request.Backend)
	if err != nil {
		return Task{}, nil, err
	}
	agentRuntime := manager.runtimes[selection.Adapter.Name()]
	now := manager.now().UTC()
	id, err := manager.newID(now)
	if err != nil {
		return Task{}, nil, err
	}
	task := Task{
		ID: id, ProjectID: project.ID, WorktreeID: request.WorktreeID, AgentKind: request.AgentKind,
		Backend: agentRuntime.Name(), State: Starting, StateSource: SourceRegistry, CWD: filepath.Clean(cwd),
		StartedAt: now, LastEventAt: now, BackendDetails: map[string]string{}, Command: []string{},
	}
	backup, err := manager.state.Create(task)
	if err != nil {
		return Task{}, nonEmpty(backup), err
	}
	started := func(reference string, details map[string]string, pid int) error {
		updated, updateBackup, updateErr := manager.state.Update(task.ID, func(current *Task) error {
			current.State = Running
			current.BackendRef = reference
			current.BackendDetails = details
			current.PID = pid
			current.LastEventAt = manager.now().UTC()
			return nil
		})
		if updateBackup != "" {
			backup = updateBackup
		}
		if updateErr == nil {
			task = updated
		}
		return updateErr
	}
	result, launchErr := agentRuntime.Launch(ctx, LaunchRequest{Task: task, Project: project, Profile: profile, Executable: executable, OnStarted: started})
	updated, updateBackup, updateErr := manager.state.Update(task.ID, func(current *Task) error {
		current.Command = append([]string(nil), result.Command...)
		current.LastEventAt = manager.now().UTC()
		if launchErr != nil {
			current.State = Failed
			code := result.ExitCode
			current.ExitCode = &code
			completed := current.LastEventAt
			current.CompletedAt = &completed
		} else if result.Waited {
			current.State = Completed
			code := result.ExitCode
			current.ExitCode = &code
			completed := current.LastEventAt
			current.CompletedAt = &completed
		}
		return nil
	})
	if updateBackup != "" {
		backup = updateBackup
	}
	if updateErr != nil {
		return task, nonEmpty(backup), &PartialError{Message: "agent launch finished but registry update failed", Task: task, Backups: nonEmpty(backup), Cause: updateErr}
	}
	task = updated
	if launchErr != nil {
		return task, nonEmpty(backup), launchErr
	}
	return task, nonEmpty(backup), nil
}

func (manager *Manager) List(ctx context.Context, projectID string) ([]Task, []string, error) {
	tasks, err := manager.state.List(projectID)
	if err != nil {
		return nil, nil, err
	}
	warnings := []string{}
	for index := range tasks {
		updated, warning, reconcileErr := manager.reconcile(ctx, tasks[index])
		if reconcileErr != nil {
			return tasks, warnings, reconcileErr
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		tasks[index] = updated
	}
	return tasks, warnings, nil
}

func (manager *Manager) Show(ctx context.Context, id string) (Task, []string, error) {
	task, found, err := manager.state.Show(id)
	if err != nil {
		return Task{}, nil, err
	}
	if !found {
		return Task{}, nil, &NotFoundError{Message: fmt.Sprintf("agent task %q was not found", id)}
	}
	updated, warning, err := manager.reconcile(ctx, task)
	if warning == "" {
		return updated, nil, err
	}
	return updated, []string{warning}, err
}

func (manager *Manager) Jump(ctx context.Context, id string) (Task, error) {
	task, _, err := manager.Show(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if !active(task.State) {
		return task, &ConflictError{Message: fmt.Sprintf("agent task %q is %s", id, task.State)}
	}
	agentRuntime, exists := manager.runtimes[task.Backend]
	if !exists {
		return task, &backend.UnavailableError{Backend: task.Backend, Reason: "agent runtime is not registered"}
	}
	return task, agentRuntime.Jump(ctx, task)
}

func (manager *Manager) Stop(ctx context.Context, id string) (Task, []string, error) {
	task, found, err := manager.state.Show(id)
	if err != nil {
		return Task{}, nil, err
	}
	if !found {
		return Task{}, nil, &NotFoundError{Message: fmt.Sprintf("agent task %q was not found", id)}
	}
	if !active(task.State) {
		return task, nil, &ConflictError{Message: fmt.Sprintf("agent task %q is %s", id, task.State)}
	}
	if task.BackendRef == "" {
		return task, nil, &ConflictError{Message: fmt.Sprintf("agent task %q has no registered backend reference", id)}
	}
	agentRuntime, exists := manager.runtimes[task.Backend]
	if !exists {
		return task, nil, &backend.UnavailableError{Backend: task.Backend, Reason: "agent runtime is not registered"}
	}
	if err := agentRuntime.Stop(ctx, task); err != nil {
		return task, nil, err
	}
	updated, backup, err := manager.state.Update(id, func(current *Task) error {
		current.State = Stopped
		current.LastEventAt = manager.now().UTC()
		completed := current.LastEventAt
		current.CompletedAt = &completed
		return nil
	})
	return updated, nonEmpty(backup), err
}

func (manager *Manager) reconcile(ctx context.Context, task Task) (Task, string, error) {
	if !active(task.State) || task.State == Starting {
		return task, "", nil
	}
	agentRuntime, exists := manager.runtimes[task.Backend]
	if !exists {
		return task, fmt.Sprintf("backend %q is not registered; task %s was not reconciled", task.Backend, task.ID), nil
	}
	alive, err := agentRuntime.Alive(ctx, task)
	var unsupported *UnsupportedError
	if errors.As(err, &unsupported) {
		return task, "", nil
	}
	if err != nil {
		return task, "", err
	}
	if alive {
		return task, "", nil
	}
	updated, _, err := manager.state.Update(task.ID, func(current *Task) error {
		current.State = Completed
		current.LastEventAt = manager.now().UTC()
		completed := current.LastEventAt
		current.CompletedAt = &completed
		return nil
	})
	return updated, "", err
}

func active(state State) bool {
	return state == Starting || state == Running || state == Waiting || state == Idle
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

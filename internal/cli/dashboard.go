package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	binboxadapter "github.com/jisung9870/workbench/adapters/binbox"
	cmuxadapter "github.com/jisung9870/workbench/adapters/cmux"
	gitadapter "github.com/jisung9870/workbench/adapters/git"
	shelladapter "github.com/jisung9870/workbench/adapters/shell"
	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	wtadapter "github.com/jisung9870/workbench/adapters/windows_terminal"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/dashboard"
	"github.com/jisung9870/workbench/internal/doctor"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/overview"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/secrets"
	sessionstate "github.com/jisung9870/workbench/internal/sessions"
	"github.com/jisung9870/workbench/internal/tasks"
	"github.com/jisung9870/workbench/internal/workflows"
	"github.com/jisung9870/workbench/internal/worktrees"
)

type dashboardService struct {
	paths     config.Paths
	tmux      tmuxRuntime
	executor  backend.Executor
	workflows workflowRuntime
	sessions  sessionRuntime
}

type sessionRuntime interface {
	Attach(context.Context, string, bool) (sessionstate.Item, error)
	Adopt(context.Context, projects.Project) (sessionstate.Item, bool, error)
	Stop(context.Context, projects.Project) (sessionstate.Item, error)
	Ensure(context.Context, projects.Project) (sessionstate.Item, bool, error)
}

type workflowRuntime interface {
	Catalog(context.Context, string) ([]workflows.Availability, error)
	Launch(context.Context, string, string) (workflows.Result, string, error)
	History(string) ([]workflows.Result, error)
	Jump(context.Context, string, bool, func(string) string) error
}

type tmuxRuntime interface {
	Snapshot(context.Context) tmuxadapter.Snapshot
	Jump(context.Context, string, bool) error
}

func runDashboard(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--open": true, "--port": true})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb dashboard [--open auto|cmux|browser|none] [--port <0-65535>]")
	}
	openTarget := options["--open"]
	if openTarget == "" {
		openTarget = "auto"
	}
	switch openTarget {
	case "auto", "cmux", "browser", "none":
	default:
		return invalid("invalid dashboard open target %q", openTarget)
	}
	port := 0
	if value := options["--port"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 65535 {
			return invalid("dashboard port must be between 0 and 65535")
		}
		port = parsed
	}
	listener, err := dashboard.Listen(port)
	if err != nil {
		return generalError(fmt.Errorf("listen for dashboard: %w", err))
	}
	token, err := dashboard.NewToken()
	if err != nil {
		_ = listener.Close()
		return generalError(err)
	}
	handler, err := dashboard.NewHandler(&dashboardService{paths: paths}, token)
	if err != nil {
		_ = listener.Close()
		return generalError(err)
	}
	dashboardURL := dashboard.URL(listener)
	fmt.Fprintf(stdout, "Workbench Dashboard\nURL: %s\nopen: %s\nPress Ctrl-C to stop.\n", dashboardURL, openTarget)
	executor := &backend.OSExecutor{Stdout: stdout, Stderr: stderr}
	environment := backend.CurrentEnvironment()
	if err := dashboard.Open(context.Background(), executor, runtime.GOOS, environment.IsWSL(), openTarget, dashboardURL); err != nil {
		fmt.Fprintf(stderr, "warning: %s; open %s manually\n", err, dashboardURL)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dashboard.Serve(ctx, listener, handler); err != nil {
		return generalError(fmt.Errorf("serve dashboard: %w", err))
	}
	return nil
}

func (service *dashboardService) Snapshot(ctx context.Context) (dashboard.Snapshot, error) {
	generatedAt := time.Now().UTC()
	executor := service.processExecutor()
	environment := backend.CurrentEnvironment()
	registry := dashboardBackendRegistry(executor, environment)
	tmuxSnapshot := service.tmuxObserver(executor).Snapshot(ctx)
	doctorReport := doctor.NewManager(service.paths, executor, environment, registry).Run(ctx, "")
	toolHealth := binboxadapter.New(executor).Doctor(ctx)
	projectStore := projects.NewStore(service.paths)
	projectItems, err := projectStore.List()
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	agentManager := newAgentManager(service.paths, io.Discard, io.Discard)
	agentItems, warnings, err := agentManager.List(ctx, "")
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	contexts, contextWarnings := buildDashboardContexts(service.paths, projectItems)
	warnings = append(warnings, contextWarnings...)
	dashboardAgents := make([]dashboard.AgentTask, 0, len(agentItems))
	for _, task := range agentItems {
		lifecycle := "terminal"
		if agents.IsActiveState(task.State) {
			lifecycle = "active"
		}
		dashboardAgents = append(dashboardAgents, dashboard.AgentTask{Task: task, Lifecycle: lifecycle})
	}
	workflowManager := service.workflowManager()
	workflowHistory, historyErr := workflowManager.History("")
	if historyErr != nil {
		warnings = append(warnings, fmt.Sprintf("workflow history: %v", historyErr))
		workflowHistory = []workflows.Result{}
	}
	unifiedTasks := tasks.ProjectWithWorkflows(agentItems, workflowHistory, tmuxSnapshot, projectItems, generatedAt)
	worktreeManager := worktrees.NewManager(projectStore, worktrees.NewStateStore(service.paths), gitadapter.New(executor))
	worktreeItems := []worktrees.Item{}
	changes := make([]dashboard.ChangeSummary, 0, len(projectItems))
	for _, project := range projectItems {
		items, listErr := worktreeManager.List(ctx, project.ID)
		if listErr != nil {
			warnings = append(warnings, fmt.Sprintf("worktrees for %s: %v", project.ID, listErr))
		} else {
			worktreeItems = append(worktreeItems, items...)
		}
		changes = append(changes, projectChanges(ctx, executor, project))
	}
	overviewSummary := overview.Build(overview.Input{
		Projects: projectItems, Tasks: unifiedTasks, Tmux: tmuxSnapshot, Worktrees: worktreeItems,
		Changes: changes, Doctor: doctorReport, Tools: toolHealth,
	})
	workflowItems := []workflows.Availability{}
	for _, project := range projectItems {
		items, catalogErr := workflowManager.Catalog(ctx, project.ID)
		if catalogErr != nil {
			warnings = append(warnings, fmt.Sprintf("workflows for %s: %v", project.ID, catalogErr))
			continue
		}
		workflowItems = append(workflowItems, items...)
	}
	safeWorkflowHistory := make([]dashboard.WorkflowRun, 0, len(workflowHistory))
	for _, item := range workflowHistory {
		safeWorkflowHistory = append(safeWorkflowHistory, dashboard.SafeWorkflowRun(item))
	}
	sort.Strings(warnings)
	return dashboard.Snapshot{
		GeneratedAt: generatedAt, Platform: doctorReport.Platform, Profile: doctorReport.Profile,
		AgentRegistryPath: service.paths.AgentsFile, Projects: projectItems, Agents: dashboardAgents, Worktrees: worktreeItems, Changes: changes,
		Doctor: doctorReport, Warnings: warnings,
		Tmux: tmuxSnapshot, Tasks: unifiedTasks, Overview: overviewSummary, ToolHealth: toolHealth,
		Workflows: workflowItems, WorkflowHistory: safeWorkflowHistory,
		Contexts: contexts,
	}, nil
}

func buildDashboardContexts(paths config.Paths, projectItems []projects.Project) (dashboard.Contexts, []string) {
	result := dashboard.Contexts{RegistryAvailable: true, Environments: []dashboard.ContextEnvironment{}}
	items, err := environments.NewStore(paths).List()
	if err != nil {
		result.RegistryAvailable = false
		result.Reason = "environment registry unavailable"
		return result, []string{"contexts: environment registry unavailable"}
	}
	return projectDashboardContexts(items, projectItems, secrets.NewStore(paths)), nil
}

type dashboardSecretLister interface {
	List(service string) ([]secrets.Entry, error)
}

func projectDashboardContexts(items []environments.Environment, projectItems []projects.Project, secretLister dashboardSecretLister) dashboard.Contexts {
	result := dashboard.Contexts{RegistryAvailable: true, Environments: []dashboard.ContextEnvironment{}}
	projectIDs := map[string][]string{}
	for _, project := range projectItems {
		if project.EnvironmentID != "" {
			projectIDs[project.EnvironmentID] = append(projectIDs[project.EnvironmentID], project.ID)
			result.Summary.LinkedProjects++
		}
	}
	for environmentID := range projectIDs {
		sort.Strings(projectIDs[environmentID])
	}
	entries, listErr := secretLister.List("")
	availableSecrets := make(map[environments.SecretReference]struct{}, len(entries))
	if listErr == nil {
		for _, entry := range entries {
			if entry.Field != "" {
				availableSecrets[environments.SecretReference{Service: entry.Service, Field: entry.Field}] = struct{}{}
			}
		}
	}
	for _, item := range items {
		exportKeys := make([]string, 0, len(item.Exports))
		for key := range item.Exports {
			exportKeys = append(exportKeys, key)
		}
		sort.Strings(exportKeys)
		projectLinks := append([]string(nil), projectIDs[item.ID]...)
		references := make([]dashboard.ContextSecretReference, 0, len(item.Secrets))
		variables := make([]string, 0, len(item.Secrets))
		for variable := range item.Secrets {
			variables = append(variables, variable)
		}
		sort.Strings(variables)
		for _, variable := range variables {
			projected := projectDashboardSecretReference(variable, item.Secrets[variable], availableSecrets, listErr)
			references = append(references, projected)
			switch projected.Status {
			case "available":
				result.Summary.Available++
			case "missing":
				result.Summary.Missing++
			case "store_unavailable":
				result.Summary.StoreUnavailable++
			default:
				result.Summary.InvalidReferences++
			}
		}
		result.Environments = append(result.Environments, dashboard.ContextEnvironment{
			ID: item.ID, AWSProfile: item.AWSProfile, AWSRegion: item.AWSRegion,
			KubeContext: item.KubeContext, KubeNamespace: item.KubeNamespace,
			ExportKeys: exportKeys, ProjectIDs: projectLinks, SecretReferences: references,
		})
		result.Summary.SecretReferences += len(references)
	}
	sort.Slice(result.Environments, func(i, j int) bool { return result.Environments[i].ID < result.Environments[j].ID })
	result.Summary.Environments = len(result.Environments)
	return result
}

func projectDashboardSecretReference(variable, referenceValue string, available map[environments.SecretReference]struct{}, storeErr error) dashboard.ContextSecretReference {
	projected := dashboard.ContextSecretReference{Variable: variable, Status: "invalid_reference"}
	reference, err := environments.ParseSecretReference(referenceValue)
	if err != nil {
		return projected
	}
	if storeErr != nil {
		projected.Status = "store_unavailable"
		return projected
	}
	if _, found := available[reference]; found {
		projected.Status = "available"
	} else {
		projected.Status = "missing"
	}
	return projected
}

func (service *dashboardService) workflowManager() workflowRuntime {
	if service.workflows != nil {
		return service.workflows
	}
	return workflows.New(service.paths)
}

func (service *dashboardService) processExecutor() backend.Executor {
	if service.executor != nil {
		return service.executor
	}
	return &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
}

func (service *dashboardService) tmuxObserver(executor backend.Executor) tmuxRuntime {
	if service.tmux != nil {
		return service.tmux
	}
	return tmuxadapter.New(executor, os.Getenv)
}

func (service *dashboardService) sessionManager(executor backend.Executor) sessionRuntime {
	if service.sessions != nil {
		return service.sessions
	}
	return sessionstate.NewManager(executor, os.Getenv)
}

func (service *dashboardService) Execute(ctx context.Context, request dashboard.ActionRequest) (dashboard.ActionResult, error) {
	switch request.Action {
	case "run_workflow":
		if request.ProjectID == "" || request.WorkflowID == "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("run_workflow requires only project_id and workflow_id")
		}
		result, _, err := service.workflowManager().Launch(ctx, request.WorkflowID, request.ProjectID)
		if err != nil {
			var invalidErr *workflows.InvalidError
			var unavailable *workflows.UnavailableError
			var notFound *workflows.NotFoundError
			var partial *workflows.PartialError
			switch {
			case errors.As(err, &invalidErr):
				return dashboard.ActionResult{}, dashboardInvalid(err.Error())
			case errors.As(err, &notFound):
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusNotFound, Code: "PROJECT_NOT_FOUND", Message: err.Error()}
			case errors.As(err, &unavailable):
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusServiceUnavailable, Code: "WORKFLOW_UNAVAILABLE", Message: err.Error()}
			case errors.As(err, &partial):
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusInternalServerError, Code: "PARTIAL_RESULT", Message: err.Error(), Details: map[string]any{"workflow_run": partial.Result, "backup": partial.Backup}}
			default:
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusInternalServerError, Code: "WORKFLOW_FAILED", Message: err.Error()}
			}
		}
		safeResult := dashboard.SafeWorkflowRun(result)
		return dashboard.ActionResult{Message: fmt.Sprintf("workflow %s launched with status %s", result.WorkflowID, result.Status), WorkflowRun: &safeResult}, nil
	case "jump_task":
		if request.TaskID == "" || len(request.TaskIDs) != 0 || request.ProjectID != "" || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("jump_task requires only task_id")
		}
		if strings.HasPrefix(request.TaskID, "run-") {
			if err := service.workflowManager().Jump(ctx, request.TaskID, false, os.Getenv); err != nil {
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusServiceUnavailable, Code: "WORKFLOW_TASK_UNAVAILABLE", Message: err.Error()}
			}
			return dashboard.ActionResult{Message: fmt.Sprintf("jumped to workflow task %s", request.TaskID)}, nil
		}
		if strings.HasPrefix(request.TaskID, "tmux:") {
			paneID := strings.TrimPrefix(request.TaskID, "tmux:")
			executor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
			snapshot := service.tmuxObserver(executor).Snapshot(ctx)
			observed := tasks.Project(nil, snapshot, nil, time.Now().UTC())
			task, found := tasks.Find(observed, request.TaskID)
			if !found || !task.CanJump || task.RuntimeLocation.PaneID != paneID {
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusServiceUnavailable, Code: "OBSERVED_TASK_UNAVAILABLE", Message: fmt.Sprintf("observed task %s is no longer present in tmux", request.TaskID)}
			}
			if err := service.tmuxObserver(executor).Jump(ctx, paneID, false); err != nil {
				return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusServiceUnavailable, Code: "TMUX_PANE_UNAVAILABLE", Message: err.Error()}
			}
			return dashboard.ActionResult{Message: fmt.Sprintf("jumped to observed task %s", request.TaskID)}, nil
		}
		task, jumpErr := newAgentManager(service.paths, io.Discard, io.Discard).Jump(ctx, request.TaskID)
		if jumpErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(jumpErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("jumped to %s with %s", task.ID, task.Backend)}, nil
	case "stop_task":
		if request.TaskID == "" || len(request.TaskIDs) != 0 || request.ProjectID != "" || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("stop_task requires only task_id")
		}
		if strings.HasPrefix(request.TaskID, "run-") {
			return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusConflict, Code: "WORKFLOW_STOP_UNAVAILABLE", Message: "workflow tasks are Workbench-managed but must be stopped from their terminal pane"}
		}
		if strings.HasPrefix(request.TaskID, "tmux:") {
			return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusConflict, Code: "TASK_UNMANAGED", Message: "observed tmux tasks are unmanaged and cannot be stopped by Workbench"}
		}
		task, _, stopErr := newAgentManager(service.paths, io.Discard, io.Discard).Stop(ctx, request.TaskID)
		if stopErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(stopErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("stopped task %s", task.ID)}, nil
	case "jump_pane":
		if request.PaneID == "" || request.ProjectID != "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind != "" || request.Backend != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("jump_pane requires only pane_id")
		}
		executor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
		if err := service.tmuxObserver(executor).Jump(ctx, request.PaneID, false); err != nil {
			return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusServiceUnavailable, Code: "TMUX_PANE_UNAVAILABLE", Message: err.Error()}
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("jumped to tmux pane %s", request.PaneID)}, nil
	case "open_project":
		return service.openProject(ctx, request)
	case "attach_session":
		if request.SessionName == "" || request.ProjectID != "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" {
			return dashboard.ActionResult{}, dashboardInvalid("attach_session requires only session_name")
		}
		item, err := service.sessionManager(service.processExecutor()).Attach(ctx, request.SessionName, false)
		if err != nil {
			return dashboard.ActionResult{}, dashboardCommandError(sessionError(err))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("attached tmux session %s", item.Name), Session: backend.Tmux, Surface: backend.Tmux}, nil
	case "adopt_session":
		if request.ProjectID == "" || request.SessionName != "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" {
			return dashboard.ActionResult{}, dashboardInvalid("adopt_session requires only project_id")
		}
		project, projectErr := sessionProject(service.paths, request.ProjectID)
		if projectErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(projectErr)
		}
		item, adopted, err := service.sessionManager(service.processExecutor()).Adopt(ctx, project)
		if err != nil {
			return dashboard.ActionResult{}, dashboardCommandError(sessionError(err))
		}
		message := fmt.Sprintf("tmux session %s was already managed", item.Name)
		if adopted {
			message = fmt.Sprintf("adopted tmux session %s", item.Name)
		}
		return dashboard.ActionResult{Message: message, Session: backend.Tmux, Surface: backend.Tmux}, nil
	case "stop_session":
		if request.ProjectID == "" || request.SessionName != "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" {
			return dashboard.ActionResult{}, dashboardInvalid("stop_session requires only project_id")
		}
		project, projectErr := sessionProject(service.paths, request.ProjectID)
		if projectErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(projectErr)
		}
		item, err := service.sessionManager(service.processExecutor()).Stop(ctx, project)
		if err != nil {
			return dashboard.ActionResult{}, dashboardCommandError(sessionError(err))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("stopped managed tmux session %s", item.Name), Session: backend.Tmux}, nil
	case "start_agent":
		if request.ProjectID == "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind == "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("start_agent requires project_id and agent_kind")
		}
		requested, err := service.agentBackend(ctx, request.ProjectID, request.Backend)
		if err != nil {
			return dashboard.ActionResult{}, err
		}
		manager := newAgentManager(service.paths, io.Discard, io.Discard)
		task, _, startErr := manager.Start(ctx, agents.StartRequest{ProjectID: request.ProjectID, AgentKind: request.AgentKind, Backend: requested})
		if startErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(startErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("started %s task %s", task.AgentKind, task.ID)}, nil
	case "jump_agent":
		if request.TaskID == "" || len(request.TaskIDs) != 0 || request.ProjectID != "" || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("jump_agent requires only task_id")
		}
		task, jumpErr := newAgentManager(service.paths, io.Discard, io.Discard).Jump(ctx, request.TaskID)
		if jumpErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(jumpErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("jumped to %s with %s", task.ID, task.Backend)}, nil
	case "stop_agent":
		if request.TaskID == "" || len(request.TaskIDs) != 0 || request.ProjectID != "" || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("stop_agent requires only task_id")
		}
		task, _, stopErr := newAgentManager(service.paths, io.Discard, io.Discard).Stop(ctx, request.TaskID)
		if stopErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(stopErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("stopped task %s", task.ID)}, nil
	case "clear_agent_history":
		if request.ProjectID == "" || request.TaskID != "" || len(request.TaskIDs) == 0 || request.AgentKind != "" || request.Backend != "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
			return dashboard.ActionResult{}, dashboardInvalid("clear_agent_history requires project_id and task_ids")
		}
		if _, found, projectErr := projects.NewStore(service.paths).Show(request.ProjectID); projectErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(configError(projectErr))
		} else if !found {
			return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusNotFound, Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q was not found", request.ProjectID)}
		}
		removed, backup, pruneErr := agents.NewStateStore(service.paths).PruneTerminal(request.ProjectID, request.TaskIDs)
		if pruneErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(pruneErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("cleared %d terminal task records for %s; backup %s", removed, request.ProjectID, backup)}, nil
	default:
		return dashboard.ActionResult{}, dashboardInvalid(fmt.Sprintf("unknown dashboard action %q", request.Action))
	}
}

func (service *dashboardService) openProject(ctx context.Context, request dashboard.ActionRequest) (dashboard.ActionResult, error) {
	if request.ProjectID == "" || request.TaskID != "" || len(request.TaskIDs) != 0 || request.AgentKind != "" || request.PaneID != "" || request.WorkflowID != "" || request.SessionName != "" {
		return dashboard.ActionResult{}, dashboardInvalid("open_project requires project_id and optional backend")
	}
	project, found, err := projects.NewStore(service.paths).Show(request.ProjectID)
	if err != nil {
		return dashboard.ActionResult{}, dashboardCommandError(configError(err))
	}
	if !found {
		return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusNotFound, Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q was not found", request.ProjectID)}
	}
	settings, err := config.LoadSettings(service.paths.ConfigFile)
	if err != nil {
		return dashboard.ActionResult{}, dashboardCommandError(configError(err))
	}
	profile, err := config.LoadProfile(service.paths, settings.ActiveProfile)
	if err != nil {
		return dashboard.ActionResult{}, dashboardCommandError(configError(err))
	}
	requested, err := backend.ParseName(defaultValue(request.Backend, string(backend.Auto)))
	if err != nil {
		return dashboard.ActionResult{}, dashboardInvalid(err.Error())
	}
	executor := service.processExecutor()
	environment := backend.CurrentEnvironment()
	openRequest := backend.OpenRequest{Project: project, Profile: profile}
	selection, err := selectDashboardOpenBackend(ctx, dashboardBackendRegistry(executor, environment), openRequest, requested, environment)
	if err != nil {
		return dashboard.ActionResult{}, dashboardCommandError(backendSelectionError(err))
	}
	if selection.Session == backend.Tmux {
		if _, _, ensureErr := service.sessionManager(executor).Ensure(ctx, project); ensureErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(sessionError(ensureErr))
		}
	}
	openRequest.Session = selection.Session
	result, err := selection.Adapter.OpenProject(ctx, openRequest)
	if err != nil {
		return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusInternalServerError, Code: "BACKEND_EXECUTION_FAILED", Message: err.Error()}
	}
	session := result.Session
	if session == "" {
		session = selection.Session
	}
	surface := result.Surface
	if surface == "" {
		surface = selection.Surface
	}
	message := fmt.Sprintf("opened %s with %s", project.ID, surface)
	if session != "" {
		message = fmt.Sprintf("opened %s with %s via %s", project.ID, session, surface)
	}
	return dashboard.ActionResult{Message: message, Session: session, Surface: surface}, nil
}

func selectDashboardOpenBackend(ctx context.Context, registry *backend.Registry, request backend.OpenRequest, requested backend.Name, environment backend.Environment) (backend.Selection, error) {
	if requested != backend.Auto {
		if requested == backend.Tmux {
			return selectDashboardTmuxSurface(ctx, registry, request, environment)
		}
		if requested != backend.CMUX && requested != backend.WindowsTerminal {
			return backend.Selection{}, fmt.Errorf("Dashboard project open supports tmux, cmux, or Windows Terminal, not %s", requested)
		}
		selection, err := registry.Select(ctx, request, requested)
		if err == nil {
			selection.Surface = selection.Adapter.Name()
		}
		return selection, err
	}

	candidates := []backend.Name{}
	seen := map[backend.Name]struct{}{}
	appendCandidate := func(name backend.Name) {
		if name != backend.Tmux && name != backend.CMUX && name != backend.WindowsTerminal {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		candidates = append(candidates, name)
	}
	appendCandidate(backend.Name(request.Project.DefaultBackend))
	appendCandidate(backend.Name(request.Profile.DefaultBackend))
	if request.Profile.PreferCurrentTmux && environment.IsWSL() {
		appendCandidate(backend.Tmux)
	}
	for _, configured := range request.Profile.BackendPriority {
		candidate := backend.Name(configured)
		if candidate == backend.CMUX && environment.IsSSH() {
			continue
		}
		appendCandidate(candidate)
	}
	if environment.GOOS == "windows" || environment.IsWSL() {
		appendCandidate(backend.WindowsTerminal)
	}
	if environment.GOOS == "darwin" && !environment.IsSSH() {
		appendCandidate(backend.CMUX)
	}

	reasons := []string{}
	for _, candidate := range candidates {
		var selection backend.Selection
		var err error
		if candidate == backend.Tmux {
			selection, err = selectDashboardTmuxSurface(ctx, registry, request, environment)
		} else {
			selection, err = registry.Select(ctx, request, candidate)
			selection.Surface = candidate
		}
		if err == nil {
			return selection, nil
		}
		var unavailable *backend.UnavailableError
		if !errors.As(err, &unavailable) {
			return backend.Selection{}, err
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s", candidate, unavailable.Reason))
	}
	reason := "no Dashboard-compatible session and terminal surface combination was detected"
	if len(reasons) > 0 {
		reason += "; " + strings.Join(reasons, "; ")
	}
	return backend.Selection{}, &backend.UnavailableError{Backend: backend.Auto, Reason: reason}
}

func selectDashboardTmuxSurface(ctx context.Context, registry *backend.Registry, request backend.OpenRequest, environment backend.Environment) (backend.Selection, error) {
	if !environment.IsWSL() {
		return backend.Selection{}, &backend.UnavailableError{Backend: backend.Tmux, Reason: "Dashboard tmux open currently requires WSL with Windows Terminal"}
	}
	if _, err := registry.Select(ctx, request, backend.Tmux); err != nil {
		return backend.Selection{}, err
	}
	request.Session = backend.Tmux
	selection, err := registry.Select(ctx, request, backend.WindowsTerminal)
	if err != nil {
		return backend.Selection{}, err
	}
	selection.Session = backend.Tmux
	selection.Surface = backend.WindowsTerminal
	return selection, nil
}

func (service *dashboardService) agentBackend(ctx context.Context, projectID, requestedValue string) (backend.Name, error) {
	project, found, err := projects.NewStore(service.paths).Show(projectID)
	if err != nil {
		return "", dashboardCommandError(configError(err))
	}
	if !found {
		return "", &dashboard.ActionError{Status: http.StatusNotFound, Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q was not found", projectID)}
	}
	settings, err := config.LoadSettings(service.paths.ConfigFile)
	if err != nil {
		return "", dashboardCommandError(configError(err))
	}
	profile, err := config.LoadProfile(service.paths, settings.ActiveProfile)
	if err != nil {
		return "", dashboardCommandError(configError(err))
	}
	requested, err := backend.ParseName(defaultValue(requestedValue, string(backend.Auto)))
	if err != nil {
		return "", dashboardInvalid(err.Error())
	}
	executor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
	selection, err := dashboardBackendRegistry(executor, backend.CurrentEnvironment()).Select(ctx, backend.OpenRequest{Project: project, Profile: profile}, requested)
	if err != nil {
		return "", dashboardCommandError(backendSelectionError(err))
	}
	if selection.Adapter.Name() == backend.Shell {
		return "", &dashboard.ActionError{
			Status: http.StatusServiceUnavailable, Code: "BACKEND_UNAVAILABLE",
			Message: "dashboard Agent start requires tmux, cmux, or Windows Terminal; interactive shell launch is not detached",
		}
	}
	return selection.Adapter.Name(), nil
}

func dashboardBackendRegistry(executor backend.Executor, environment backend.Environment) *backend.Registry {
	return backend.NewRegistry(environment,
		shelladapter.New(executor, shelladapter.Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}),
		tmuxadapter.New(executor, os.Getenv),
		cmuxadapter.New(executor, runtime.GOOS),
		wtadapter.New(executor, wtadapter.Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}),
	)
}

func projectChanges(ctx context.Context, executor backend.Executor, project projects.Project) dashboard.ChangeSummary {
	summary := dashboard.ChangeSummary{ProjectID: project.ID, ChangedFiles: []string{}}
	command, err := executor.LookPath("git")
	if err != nil {
		summary.Unavailable = "git executable was not found"
		return summary
	}
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := executor.Run(statusCtx, backend.ProcessRequest{Name: command, Args: []string{"-C", project.RepoRoot, "status", "--porcelain=v1", "--branch", "--untracked-files=normal"}})
	if err != nil {
		summary.Unavailable = strings.TrimSpace(result.Stderr)
		if summary.Unavailable == "" {
			summary.Unavailable = err.Error()
		}
		return summary
	}
	for index, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if line == "" {
			continue
		}
		if index == 0 && strings.HasPrefix(line, "## ") {
			branch := strings.TrimPrefix(line, "## ")
			branch = strings.SplitN(branch, "...", 2)[0]
			branch = strings.SplitN(branch, " ", 2)[0]
			summary.Branch = branch
			continue
		}
		if len(line) > 3 {
			name := strings.TrimSpace(line[3:])
			if parts := strings.Split(name, " -> "); len(parts) == 2 {
				name = parts[1]
			}
			summary.ChangedFiles = append(summary.ChangedFiles, name)
		}
	}
	summary.Changed = len(summary.ChangedFiles)
	summary.Dirty = summary.Changed > 0
	return summary
}

func backendSelectionError(err error) *commandError {
	var unavailable *backend.UnavailableError
	if errors.As(err, &unavailable) {
		return &commandError{ExitCode: ExitUnavailable, Code: "BACKEND_UNAVAILABLE", Message: unavailable.Error()}
	}
	return invalid("%s", err)
}

func dashboardCommandError(err *commandError) error {
	status := http.StatusInternalServerError
	switch err.ExitCode {
	case ExitArgument:
		status = http.StatusBadRequest
	case ExitUnavailable:
		status = http.StatusServiceUnavailable
	case ExitConflict, ExitPartial:
		status = http.StatusConflict
	}
	return &dashboard.ActionError{Status: status, Code: err.Code, Message: err.Message, Details: err.Details}
}

func dashboardInvalid(message string) error {
	return &dashboard.ActionError{Status: http.StatusBadRequest, Code: "INVALID_ACTION", Message: message}
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

var _ dashboard.Service = (*dashboardService)(nil)

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
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/worktrees"
)

type dashboardService struct {
	paths config.Paths
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
	executor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
	environment := backend.CurrentEnvironment()
	registry := dashboardBackendRegistry(executor, environment)
	doctorReport := doctor.NewManager(service.paths, executor, environment, registry).Run(ctx, "")
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
	sort.Strings(warnings)
	return dashboard.Snapshot{
		GeneratedAt: time.Now().UTC(), Platform: doctorReport.Platform, Profile: doctorReport.Profile,
		Projects: projectItems, Agents: agentItems, Worktrees: worktreeItems, Changes: changes,
		Doctor: doctorReport, Warnings: warnings,
	}, nil
}

func (service *dashboardService) Execute(ctx context.Context, request dashboard.ActionRequest) (dashboard.ActionResult, error) {
	switch request.Action {
	case "open_project":
		return service.openProject(ctx, request)
	case "start_agent":
		if request.ProjectID == "" || request.TaskID != "" || request.AgentKind == "" {
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
		if request.TaskID == "" || request.ProjectID != "" || request.AgentKind != "" || request.Backend != "" {
			return dashboard.ActionResult{}, dashboardInvalid("jump_agent requires only task_id")
		}
		task, jumpErr := newAgentManager(service.paths, io.Discard, io.Discard).Jump(ctx, request.TaskID)
		if jumpErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(jumpErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("jumped to %s with %s", task.ID, task.Backend)}, nil
	case "stop_agent":
		if request.TaskID == "" || request.ProjectID != "" || request.AgentKind != "" || request.Backend != "" {
			return dashboard.ActionResult{}, dashboardInvalid("stop_agent requires only task_id")
		}
		task, _, stopErr := newAgentManager(service.paths, io.Discard, io.Discard).Stop(ctx, request.TaskID)
		if stopErr != nil {
			return dashboard.ActionResult{}, dashboardCommandError(agentError(stopErr))
		}
		return dashboard.ActionResult{Message: fmt.Sprintf("stopped task %s", task.ID)}, nil
	default:
		return dashboard.ActionResult{}, dashboardInvalid(fmt.Sprintf("unknown dashboard action %q", request.Action))
	}
}

func (service *dashboardService) openProject(ctx context.Context, request dashboard.ActionRequest) (dashboard.ActionResult, error) {
	if request.ProjectID == "" || request.TaskID != "" || request.AgentKind != "" {
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
	executor := &backend.OSExecutor{Stdout: io.Discard, Stderr: io.Discard}
	environment := backend.CurrentEnvironment()
	selection, err := selectDashboardOpenBackend(ctx, dashboardBackendRegistry(executor, environment), backend.OpenRequest{Project: project, Profile: profile}, requested, environment)
	if err != nil {
		return dashboard.ActionResult{}, dashboardCommandError(backendSelectionError(err))
	}
	result, err := selection.Adapter.OpenProject(ctx, backend.OpenRequest{Project: project, Profile: profile})
	if err != nil {
		return dashboard.ActionResult{}, &dashboard.ActionError{Status: http.StatusInternalServerError, Code: "BACKEND_EXECUTION_FAILED", Message: err.Error()}
	}
	return dashboard.ActionResult{Message: fmt.Sprintf("opened %s with %s", project.ID, result.Backend)}, nil
}

func selectDashboardOpenBackend(ctx context.Context, registry *backend.Registry, request backend.OpenRequest, requested backend.Name, environment backend.Environment) (backend.Selection, error) {
	if requested != backend.Auto {
		if requested != backend.CMUX && requested != backend.WindowsTerminal {
			return backend.Selection{}, fmt.Errorf("Dashboard project open supports only cmux or Windows Terminal, not %s", requested)
		}
		return registry.Select(ctx, request, requested)
	}

	candidates := []backend.Name{}
	seen := map[backend.Name]struct{}{}
	appendCandidate := func(name backend.Name) {
		if name != backend.CMUX && name != backend.WindowsTerminal {
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
		selection, err := registry.Select(ctx, request, candidate)
		if err == nil {
			return selection, nil
		}
		var unavailable *backend.UnavailableError
		if !errors.As(err, &unavailable) {
			return backend.Selection{}, err
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s", candidate, unavailable.Reason))
	}
	reason := "no Dashboard-compatible backend was detected; use the CLI for tmux or shell project open"
	if len(reasons) > 0 {
		reason += "; " + strings.Join(reasons, "; ")
	}
	return backend.Selection{}, &backend.UnavailableError{Backend: backend.Auto, Reason: reason}
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

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	cmuxadapter "github.com/jisung9870/workbench/adapters/cmux"
	gitadapter "github.com/jisung9870/workbench/adapters/git"
	shelladapter "github.com/jisung9870/workbench/adapters/shell"
	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	wtadapter "github.com/jisung9870/workbench/adapters/windows_terminal"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/compatibility"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/doctor"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/migrate"
	"github.com/jisung9870/workbench/internal/output"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/secrets"
	"github.com/jisung9870/workbench/internal/tasks"
	"github.com/jisung9870/workbench/internal/workflows"
	"github.com/jisung9870/workbench/internal/worktrees"
)

const (
	ExitOK          = 0
	ExitGeneral     = 1
	ExitArgument    = 2
	ExitUnavailable = 3
	ExitConflict    = 4
	ExitPartial     = 5
)

type commandError struct {
	ExitCode int
	Code     string
	Message  string
	Details  map[string]any
	Reported bool
}

func (err *commandError) Error() string { return err.Message }

func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithInput(args, os.Stdin, stdout, stderr)
}

func RunWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	jsonMode := contains(args, "--json")
	paths, err := config.ResolvePaths()
	if err != nil {
		return report(stdout, stderr, jsonMode, &commandError{ExitCode: ExitArgument, Code: "CONFIG_INVALID", Message: err.Error()})
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage())
		return ExitArgument
	}
	var commandErr *commandError
	switch args[0] {
	case "projects":
		commandErr = runProjects(args[1:], paths, stdout)
	case "env":
		commandErr = runEnv(args[1:], paths, stdout, stderr)
	case "secrets":
		commandErr = runSecrets(args[1:], paths, stdin, stdout, stderr)
	case "config":
		commandErr = runConfig(args[1:], paths, stdout)
	case "migrate":
		commandErr = runMigrate(args[1:], paths, stdout, stderr)
	case "open":
		commandErr = runOpen(args[1:], paths, stdout, stderr)
	case "worktrees":
		commandErr = runWorktrees(args[1:], paths, stdout, stderr)
	case "agents":
		commandErr = runAgents(args[1:], paths, stdout, stderr)
	case "tasks":
		commandErr = runTasks(args[1:], paths, stdout, stderr)
	case "sessions":
		commandErr = runSessions(args[1:], stdout, stderr)
	case "overview":
		commandErr = runOverview(args[1:], paths, stdout, stderr)
	case "workflows":
		commandErr = runWorkflows(args[1:], paths, stdout)
	case "compatibility":
		commandErr = runCompatibility(args[1:], paths, stdout)
	case "doctor":
		commandErr = runDoctor(args[1:], paths, stdout, stderr)
	case "dashboard":
		commandErr = runDashboard(args[1:], paths, stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage())
		return ExitOK
	default:
		commandErr = invalid("unknown command %q", args[0])
	}
	if commandErr != nil {
		return report(stdout, stderr, jsonMode, commandErr)
	}
	return ExitOK
}

func runWorkflows(args []string, paths config.Paths, stdout io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("workflows subcommand is required")
	}
	manager := workflows.New(paths)
	switch args[0] {
	case "catalog", "list":
		positionals, options, err := parseOptions(args[1:], map[string]bool{"--project": true, "--json": false})
		if err != nil || len(positionals) != 0 {
			return invalid("usage: wb workflows catalog [--project <id>] [--json]")
		}
		items, catalogErr := manager.Catalog(context.Background(), options["--project"])
		if catalogErr != nil {
			return workflowError(catalogErr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"workflows": items}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.ID, item.Status, item.Risk, item.Reason)
		}
		return nil
	case "run":
		positionals, options, err := parseOptions(args[1:], map[string]bool{"--project": true, "--json": false})
		if err != nil || len(positionals) != 1 || options["--project"] == "" {
			return invalid("usage: wb workflows run <workflow-id> --project <id> [--json]")
		}
		result, backup, runErr := manager.Launch(context.Background(), positionals[0], options["--project"])
		if runErr != nil {
			return workflowError(runErr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"workflow_run": result}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\texit=%v\n", result.ID, result.WorkflowID, result.Status, result.ExitCode)
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "worker":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb workflows worker <run-id>")
		}
		workerCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, _, workerErr := manager.Worker(workerCtx, args[1])
		if workerErr != nil {
			return workflowError(workerErr)
		}
		if result.Status != workflows.Succeeded {
			return &commandError{ExitCode: ExitGeneral, Code: "WORKFLOW_FAILED", Message: fmt.Sprintf("workflow %s finished with status %s", result.WorkflowID, result.Status)}
		}
		return nil
	case "history":
		positionals, options, err := parseOptions(args[1:], map[string]bool{"--project": true, "--json": false})
		if err != nil || len(positionals) != 0 {
			return invalid("usage: wb workflows history [--project <id>] [--json]")
		}
		items, historyErr := manager.History(options["--project"])
		if historyErr != nil {
			return generalError(historyErr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"workflow_runs": items}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.ID, item.ProjectID, item.WorkflowID, item.Status)
		}
		return nil
	case "show":
		positionals, options, err := parseOptions(args[1:], map[string]bool{"--json": false})
		if err != nil || len(positionals) != 1 {
			return invalid("usage: wb workflows show <run-id> [--json]")
		}
		item, found, showErr := manager.Show(positionals[0])
		if showErr != nil {
			return generalError(showErr)
		}
		if !found {
			return &commandError{ExitCode: ExitUnavailable, Code: "WORKFLOW_RUN_NOT_FOUND", Message: fmt.Sprintf("workflow run %q was not found", positionals[0])}
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"workflow_run": item}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "id: %s\nworkflow_id: %s\nproject_id: %s\nstatus: %s\nstarted_at: %s\nfinished_at: %s\n", item.ID, item.WorkflowID, item.ProjectID, item.Status, item.StartedAt.Format(time.RFC3339), item.FinishedAt.Format(time.RFC3339))
		if item.Output != "" {
			fmt.Fprint(stdout, item.Output)
		}
		return nil
	default:
		return invalid("unknown workflows subcommand %q", args[0])
	}
}

func workflowError(err error) *commandError {
	var invalidErr *workflows.InvalidError
	if errors.As(err, &invalidErr) {
		return invalid("%s", err)
	}
	var notFound *workflows.NotFoundError
	if errors.As(err, &notFound) {
		return &commandError{ExitCode: ExitUnavailable, Code: "PROJECT_NOT_FOUND", Message: err.Error()}
	}
	var unavailable *workflows.UnavailableError
	if errors.As(err, &unavailable) {
		return &commandError{ExitCode: ExitUnavailable, Code: "WORKFLOW_UNAVAILABLE", Message: err.Error()}
	}
	var partial *workflows.PartialError
	if errors.As(err, &partial) {
		return &commandError{ExitCode: ExitPartial, Code: "PARTIAL_RESULT", Message: err.Error(), Details: map[string]any{"workflow_run": partial.Result, "backup": partial.Backup}}
	}
	var conflict *workflows.ConflictError
	if errors.As(err, &conflict) {
		return &commandError{ExitCode: ExitConflict, Code: "WORKFLOW_CONFLICT", Message: err.Error()}
	}
	return generalError(err)
}

func runOverview(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--json": false})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb overview [--json]")
	}
	snapshot, err := (&dashboardService{paths: paths}).Snapshot(context.Background())
	if err != nil {
		return generalError(err)
	}
	if _, jsonMode := options["--json"]; jsonMode {
		if err := output.Write(stdout, map[string]any{"overview": snapshot.Overview}, snapshot.Warnings); err != nil {
			return generalError(err)
		}
		return nil
	}
	for _, warning := range snapshot.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	counts := snapshot.Overview.Counts
	fmt.Fprintf(stdout, "projects: %d\ntmux sessions: %d (%d attached, %d detached)\nactive tasks: %d managed, %d observed\nworktrees: %d (%d dirty)\nattention: %d\nbinbox health: %s\n",
		counts.Projects, counts.TmuxSessions, counts.AttachedSessions, counts.DetachedSessions,
		counts.ActiveManagedTasks, counts.ActiveObservedTasks, counts.Worktrees, counts.DirtyWorktrees,
		len(snapshot.Overview.Attention), availability(snapshot.Overview.ToolHealth.Available))
	for _, item := range snapshot.Overview.Attention {
		fmt.Fprintf(stdout, "- [%s] %s: %s\n", item.Severity, item.Title, item.Reason)
	}
	return nil
}

func availability(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}

func runTasks(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("tasks subcommand is required")
	}
	ctx := context.Background()
	executor := &backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	observer := tmuxadapter.New(executor, os.Getenv)
	list := func(projectID string) ([]tasks.Task, []string, error) {
		projectItems, err := projects.NewStore(paths).List()
		if err != nil {
			return nil, nil, err
		}
		managed, warnings, err := newAgentManager(paths, stdout, stderr).List(ctx, "")
		if err != nil {
			return nil, warnings, err
		}
		snapshot := observer.Snapshot(ctx)
		if !snapshot.Available {
			warnings = append(warnings, snapshot.Reason)
		}
		workflowRuns, workflowErr := workflows.New(paths).History("")
		if workflowErr != nil {
			warnings = append(warnings, fmt.Sprintf("workflow history: %v", workflowErr))
			workflowRuns = []workflows.Result{}
		}
		projected := tasks.ProjectWithWorkflows(managed, workflowRuns, snapshot, projectItems, time.Now().UTC())
		if projectID == "" {
			return projected, warnings, nil
		}
		filtered := make([]tasks.Task, 0, len(projected))
		for _, task := range projected {
			if task.ProjectID == projectID {
				filtered = append(filtered, task)
			}
		}
		return filtered, warnings, nil
	}
	switch args[0] {
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--project": true, "--json": false})
		if parseErr != nil || len(positionals) != 0 {
			return invalid("usage: wb tasks list [--project <id>] [--json]")
		}
		items, warnings, err := list(options["--project"])
		if err != nil {
			return generalError(err)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"tasks": items}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
		for _, task := range items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", task.ID, task.ProjectID, task.Kind, task.Provenance, task.Lifecycle, task.CWD)
		}
		return nil
	case "show":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb tasks show <task-id> [--json]")
		}
		items, warnings, err := list("")
		if err != nil {
			return generalError(err)
		}
		task, found := tasks.Find(items, positionals[0])
		if !found {
			return &commandError{ExitCode: ExitUnavailable, Code: "TASK_UNAVAILABLE", Message: fmt.Sprintf("task %q is not present in the current view", positionals[0])}
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"task": task}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "id: %s\nkind: %s\nprovenance: %s\nownership: %s\nconfidence: %s\nlifecycle: %s\nstate_source: %s\nproject_id: %s\ncwd: %s\nexit_result: %s\n", task.ID, task.Kind, task.Provenance, task.Ownership, task.Confidence, task.Lifecycle, task.StateSource, task.ProjectID, task.CWD, task.ExitResult)
		return nil
	case "jump":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb tasks jump <task-id>")
		}
		if strings.HasPrefix(args[1], "run-") {
			if err := workflows.New(paths).Jump(ctx, args[1], true, os.Getenv); err != nil {
				return workflowError(err)
			}
			fmt.Fprintf(stdout, "jumped to workflow task %s\n", args[1])
			return nil
		}
		if strings.HasPrefix(args[1], "tmux:") {
			items := tasks.Project(nil, observer.Snapshot(ctx), nil, time.Now().UTC())
			task, found := tasks.Find(items, args[1])
			if !found || !task.CanJump {
				return &commandError{ExitCode: ExitUnavailable, Code: "TASK_UNAVAILABLE", Message: fmt.Sprintf("observed task %q is no longer present in tmux", args[1])}
			}
			if err := observer.Jump(ctx, task.RuntimeLocation.PaneID, true); err != nil {
				return &commandError{ExitCode: ExitUnavailable, Code: "TMUX_PANE_UNAVAILABLE", Message: err.Error()}
			}
			fmt.Fprintf(stdout, "jumped to observed task %s\n", task.ID)
			return nil
		}
		task, err := newAgentManager(paths, stdout, stderr).Jump(ctx, args[1])
		if err != nil {
			return agentError(err)
		}
		fmt.Fprintf(stdout, "jumped to %s with %s\n", task.ID, task.Backend)
		return nil
	case "stop":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb tasks stop <task-id>")
		}
		if strings.HasPrefix(args[1], "run-") {
			return &commandError{ExitCode: ExitConflict, Code: "WORKFLOW_STOP_UNAVAILABLE", Message: "workflow tasks are Workbench-managed but must be stopped from their terminal pane"}
		}
		if strings.HasPrefix(args[1], "tmux:") {
			return &commandError{ExitCode: ExitConflict, Code: "TASK_UNMANAGED", Message: "observed tmux tasks are unmanaged and cannot be stopped by Workbench"}
		}
		task, backups, err := newAgentManager(paths, stdout, stderr).Stop(ctx, args[1])
		if err != nil {
			return agentError(err)
		}
		fmt.Fprintf(stdout, "stopped %s with %s\n", task.ID, task.Backend)
		for _, backup := range backups {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	default:
		return invalid("unknown tasks subcommand %q", args[0])
	}
}

func runSessions(args []string, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("sessions subcommand is required")
	}
	executor := &backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	observer := tmuxadapter.New(executor, os.Getenv)
	switch args[0] {
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 0 {
			return invalid("usage: wb sessions list [--json]")
		}
		snapshot := observer.Snapshot(context.Background())
		warnings := []string{}
		if !snapshot.Available {
			warnings = append(warnings, snapshot.Reason)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"tmux": snapshot}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		if !snapshot.Available {
			fmt.Fprintf(stdout, "tmux unavailable: %s\n", snapshot.Reason)
			return nil
		}
		for _, session := range snapshot.Sessions {
			attached := "detached"
			if session.Attached {
				attached = "attached"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", session.ID, session.Name, attached)
			for _, window := range session.Windows {
				for _, pane := range window.Panes {
					fmt.Fprintf(stdout, "  %s\t%s:%d.%d\t%s\t%s\n", pane.ID, session.Name, window.Index, pane.Index, pane.CurrentCommand, pane.CurrentPath)
				}
			}
		}
		return nil
	case "jump":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb sessions jump <pane-id>")
		}
		if err := observer.Jump(context.Background(), args[1], true); err != nil {
			return &commandError{ExitCode: ExitUnavailable, Code: "TMUX_PANE_UNAVAILABLE", Message: err.Error()}
		}
		fmt.Fprintf(stdout, "jumped to tmux pane %s\n", args[1])
		return nil
	default:
		return invalid("unknown sessions subcommand %q", args[0])
	}
}

func runDoctor(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--profile": true, "--json": false, "--strict": false})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb doctor [--profile <name>] [--json] [--strict]")
	}
	profileName := options["--profile"]
	if profileName != "" && !config.ValidProfileName(profileName) {
		return invalid("invalid profile name %q", profileName)
	}
	executor := &backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	environment := backend.CurrentEnvironment()
	registry := backend.NewRegistry(environment,
		shelladapter.New(executor, shelladapter.Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}),
		tmuxadapter.New(executor, os.Getenv),
		cmuxadapter.New(executor, runtime.GOOS),
		wtadapter.New(executor, wtadapter.Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}),
	)
	report := doctor.NewManager(paths, executor, environment, registry).Run(context.Background(), profileName)
	_, strict := options["--strict"]
	_, jsonMode := options["--json"]
	warnings := report.Warnings()
	if jsonMode {
		if report.Healthy(strict) {
			if err := output.Write(stdout, report, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		code, message, missing := doctorFailure(report, strict)
		if err := output.WriteResult(stdout, false, report, warnings, &output.Error{
			Code: code, Message: message, Details: map[string]any{"capabilities": missing, "strict": strict},
		}); err != nil {
			return generalError(err)
		}
		return &commandError{ExitCode: ExitGeneral, Code: code, Message: message, Reported: true}
	}

	fmt.Fprintf(stdout, "Workbench doctor\nplatform: %s\nprofile: %s\n\n", report.Platform, report.Profile)
	for _, capability := range report.Capabilities {
		marker := "✓"
		if capability.Status == doctor.Skipped {
			marker = "-"
		} else if capability.Status == doctor.Unavailable {
			marker = "!"
			if capability.Scope == doctor.Core {
				marker = "✗"
			}
		}
		fmt.Fprintf(stdout, "%s %-28s %-8s %s", marker, capability.Name, capability.Scope, capability.Status)
		if capability.Version != "" {
			fmt.Fprintf(stdout, " (%s)", capability.Version)
		}
		fmt.Fprintln(stdout)
		if capability.Reason != "" && capability.Status != doctor.Skipped {
			fmt.Fprintf(stdout, "  reason: %s\n", capability.Reason)
		}
		if capability.Recovery != "" && capability.Status == doctor.Unavailable {
			fmt.Fprintf(stdout, "  recovery: %s\n", capability.Recovery)
		}
	}
	fmt.Fprintf(stdout, "\navailable=%d core_missing=%d optional_missing=%d skipped=%d\n",
		report.Summary.Available, report.Summary.UnavailableCore, report.Summary.UnavailableOption, report.Summary.Skipped)
	if report.Healthy(strict) {
		if report.Summary.UnavailableOption > 0 {
			fmt.Fprintln(stdout, "core capabilities ready; optional capabilities are unavailable")
		} else {
			fmt.Fprintln(stdout, "all checked capabilities ready")
		}
		return nil
	}
	code, message, missing := doctorFailure(report, strict)
	return &commandError{ExitCode: ExitGeneral, Code: code, Message: message, Details: map[string]any{"capabilities": missing, "strict": strict}}
}

func doctorFailure(report doctor.Report, strict bool) (string, string, []string) {
	if missing := report.Missing(doctor.Core); len(missing) > 0 {
		return "CORE_CAPABILITY_UNAVAILABLE", "required Workbench capabilities are unavailable", missing
	}
	if strict {
		missing := report.Missing(doctor.Optional)
		return "OPTIONAL_CAPABILITY_UNAVAILABLE", "optional Workbench capabilities are unavailable in strict mode", missing
	}
	return "DOCTOR_FAILED", "Workbench doctor failed", []string{}
}

func runAgents(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("agents subcommand is required")
	}
	manager := newAgentManager(paths, stdout, stderr)
	ctx := context.Background()
	switch args[0] {
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--project": true, "--json": false})
		if parseErr != nil || len(positionals) != 0 {
			return invalid("usage: wb agents list [--project <id>] [--json]")
		}
		tasks, warnings, err := manager.List(ctx, options["--project"])
		if err != nil {
			return agentError(err)
		}
		if err := compatibility.NewStore(paths.CompatibilityDir).Observe("workbench", "agents", "registry", time.Now().UTC()); err != nil {
			warnings = append(warnings, fmt.Sprintf("record Agent registry observation: %v", err))
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"agents": tasks}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
		for _, task := range tasks {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", task.ID, task.ProjectID, task.AgentKind, task.Backend, task.State, task.CWD)
		}
		return nil
	case "show":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb agents show <task-id> [--json]")
		}
		task, warnings, err := manager.Show(ctx, positionals[0])
		if err != nil {
			return agentError(err)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"agent": task}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
		fmt.Fprintf(stdout, "id: %s\nproject_id: %s\nworktree_id: %s\nagent_kind: %s\nbackend: %s\nbackend_ref: %s\nstate: %s\nstate_source: %s\ncwd: %s\nstarted_at: %s\nlast_event_at: %s\n",
			task.ID, task.ProjectID, task.WorktreeID, task.AgentKind, task.Backend, task.BackendRef, task.State, task.StateSource, task.CWD, task.StartedAt.Format(time.RFC3339), task.LastEventAt.Format(time.RFC3339))
		return nil
	case "start":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--agent": true, "--worktree": true, "--backend": true})
		if parseErr != nil || len(positionals) != 1 || options["--agent"] == "" {
			return invalid("usage: wb agents start <project-id> --agent codex|claude [--worktree <id>] [--backend <backend>]")
		}
		requested := backend.Auto
		if value := options["--backend"]; value != "" {
			parsed, err := backend.ParseName(value)
			if err != nil {
				return invalid("%s", err)
			}
			requested = parsed
		}
		task, backups, err := manager.Start(ctx, agents.StartRequest{ProjectID: positionals[0], WorktreeID: options["--worktree"], AgentKind: options["--agent"], Backend: requested})
		if err != nil {
			return agentError(err)
		}
		fmt.Fprintf(stdout, "%s %s\t%s\t%s\t%s\n", task.State, task.ID, task.AgentKind, task.Backend, task.CWD)
		for _, backup := range backups {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "jump":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb agents jump <task-id>")
		}
		task, err := manager.Jump(ctx, args[1])
		if err != nil {
			return agentError(err)
		}
		fmt.Fprintf(stdout, "jumped to %s with %s\n", task.ID, task.Backend)
		return nil
	case "stop":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb agents stop <task-id>")
		}
		task, backups, err := manager.Stop(ctx, args[1])
		if err != nil {
			return agentError(err)
		}
		fmt.Fprintf(stdout, "stopped %s with %s\n", task.ID, task.Backend)
		for _, backup := range backups {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	default:
		return invalid("unknown agents subcommand %q", args[0])
	}
}

func runCompatibility(args []string, paths config.Paths, stdout io.Writer) *commandError {
	if len(args) == 0 || args[0] != "observe" {
		return invalid("usage: wb compatibility observe --client <client> --feature <feature> --source <source>")
	}
	positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--client": true, "--feature": true, "--source": true})
	if parseErr != nil || len(positionals) != 0 || options["--client"] == "" || options["--feature"] == "" || options["--source"] == "" {
		return invalid("usage: wb compatibility observe --client <client> --feature <feature> --source <source>")
	}
	client, feature, source := options["--client"], options["--feature"], options["--source"]
	if err := compatibility.ValidateExternal(client, feature, source); err != nil {
		return invalid("%s", err)
	}
	if err := compatibility.NewStore(paths.CompatibilityDir).Observe(client, feature, source, time.Now().UTC()); err != nil {
		return generalError(err)
	}
	fmt.Fprintf(stdout, "observed %s/%s/%s\n", client, feature, source)
	return nil
}

func newAgentManager(paths config.Paths, stdout, stderr io.Writer) *agents.Manager {
	executor := &backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	projectStore := projects.NewStore(paths)
	worktreeManager := worktrees.NewManager(projectStore, worktrees.NewStateStore(paths), gitadapter.New(executor))
	environment := backend.CurrentEnvironment()
	return agents.NewManager(paths, projectStore, worktreeManager, agents.NewStateStore(paths), executor, environment,
		agents.NewShellRuntime(executor),
		agents.NewTmuxRuntime(executor, os.Getenv),
		agents.NewCMUXRuntime(executor, runtime.GOOS),
		agents.NewWindowsTerminalRuntime(executor, runtime.GOOS, os.Getenv),
	)
}

func agentError(err error) *commandError {
	var invalidErr *agents.InvalidError
	if errors.As(err, &invalidErr) {
		return &commandError{ExitCode: ExitArgument, Code: "INVALID_ARGUMENT", Message: invalidErr.Error()}
	}
	var notFoundErr *agents.NotFoundError
	if errors.As(err, &notFoundErr) {
		return &commandError{ExitCode: ExitGeneral, Code: "AGENT_NOT_FOUND", Message: notFoundErr.Error()}
	}
	var conflictErr *agents.ConflictError
	var worktreeConflict *worktrees.ConflictError
	var unsafeErr *agents.UnsafeError
	if errors.As(err, &conflictErr) || errors.As(err, &worktreeConflict) || errors.As(err, &unsafeErr) {
		return &commandError{ExitCode: ExitConflict, Code: "UNSAFE_AGENT_STATE", Message: err.Error()}
	}
	var unavailableErr *backend.UnavailableError
	var unsupportedErr *agents.UnsupportedError
	if errors.As(err, &unavailableErr) || errors.As(err, &unsupportedErr) {
		return &commandError{ExitCode: ExitUnavailable, Code: "BACKEND_UNAVAILABLE", Message: err.Error()}
	}
	var partialErr *agents.PartialError
	if errors.As(err, &partialErr) {
		return &commandError{ExitCode: ExitPartial, Code: "PARTIAL_RESULT", Message: partialErr.Error(), Details: map[string]any{"agent": partialErr.Task, "backups": partialErr.Backups}}
	}
	return generalError(err)
}

func runWorktrees(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("worktrees subcommand is required")
	}
	executor := &backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	manager := worktrees.NewManager(
		projects.NewStore(paths),
		worktrees.NewStateStore(paths),
		gitadapter.New(executor),
	)
	switch args[0] {
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb worktrees list <project-id> [--json]")
		}
		items, err := manager.List(context.Background(), positionals[0])
		if err != nil {
			return worktreeError(err, stderr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"worktrees": items}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, item := range items {
			state := "clean"
			if item.Dirty {
				state = "dirty"
			}
			ownership := "external"
			if item.Managed {
				ownership = "managed"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.Branch, state, ownership, item.Path)
		}
		return nil
	case "create":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--base": true})
		if parseErr != nil || len(positionals) != 2 {
			return invalid("usage: wb worktrees create <project-id> <branch> [--base <ref>]")
		}
		item, err := manager.Create(context.Background(), positionals[0], positionals[1], options["--base"])
		if err != nil {
			return worktreeError(err, stderr)
		}
		fmt.Fprintf(stdout, "created %s\t%s\t%s\n", item.ID, item.Branch, item.Path)
		return nil
	case "remove":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--delete-branch": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb worktrees remove <worktree-id> [--delete-branch]")
		}
		_, deleteBranch := options["--delete-branch"]
		item, backups, err := manager.Remove(context.Background(), positionals[0], worktrees.RemoveOptions{
			DeleteBranch: deleteBranch,
			Confirm: func(branch string) bool {
				return confirmBranch(os.Stdin, stderr, branch)
			},
		})
		if err != nil {
			return worktreeError(err, stderr)
		}
		fmt.Fprintf(stdout, "removed %s\t%s\n", item.ID, item.Path)
		if deleteBranch {
			fmt.Fprintf(stdout, "deleted branch %s\n", item.Branch)
		}
		for _, backup := range backups {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	default:
		return invalid("unknown worktrees subcommand %q", args[0])
	}
}

func confirmBranch(reader io.Reader, writer io.Writer, branch string) bool {
	fmt.Fprintf(writer, "type branch name %q to confirm worktree and branch deletion: ", branch)
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		return false
	}
	return strings.TrimSpace(scanner.Text()) == branch
}

func worktreeError(err error, stderr io.Writer) *commandError {
	var commandErr *gitadapter.CommandError
	if errors.As(err, &commandErr) && commandErr.Result.Stderr != "" {
		fmt.Fprint(stderr, commandErr.Result.Stderr)
		if !strings.HasSuffix(commandErr.Result.Stderr, "\n") {
			fmt.Fprintln(stderr)
		}
	}
	var invalidErr *worktrees.InvalidError
	if errors.As(err, &invalidErr) {
		return &commandError{ExitCode: ExitArgument, Code: "INVALID_ARGUMENT", Message: invalidErr.Error()}
	}
	var conflictErr *worktrees.ConflictError
	if errors.As(err, &conflictErr) {
		return &commandError{ExitCode: ExitConflict, Code: "WORKTREE_CONFLICT", Message: conflictErr.Error()}
	}
	var partialErr *worktrees.PartialError
	if errors.As(err, &partialErr) {
		return &commandError{
			ExitCode: ExitPartial, Code: "PARTIAL_RESULT", Message: partialErr.Error(),
			Details: map[string]any{"worktree": partialErr.Item, "backups": partialErr.Backups},
		}
	}
	return generalError(err)
}

func runOpen(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--backend": true, "--window": true, "--terminal-mode": true})
	if parseErr != nil || len(positionals) != 1 {
		return invalid("usage: wb open <project-id> [--backend <backend>] [--window <last|new|id>] [--terminal-mode <mode>]")
	}
	requested := backend.Auto
	if value := options["--backend"]; value != "" {
		parsed, backendErr := backend.ParseName(value)
		if backendErr != nil {
			return invalid("%s", backendErr)
		}
		requested = parsed
	}
	project, found, loadErr := projects.NewStore(paths).Show(positionals[0])
	if loadErr != nil {
		return configError(loadErr)
	}
	if !found {
		return &commandError{ExitCode: ExitGeneral, Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q was not found", positionals[0]), Details: map[string]any{"project_id": positionals[0]}}
	}
	settings, settingsErr := config.LoadSettings(paths.ConfigFile)
	if settingsErr != nil {
		return configError(settingsErr)
	}
	profile, profileErr := config.LoadProfile(paths, settings.ActiveProfile)
	if profileErr != nil {
		return configError(profileErr)
	}
	_, windowOverride := options["--window"]
	if windowOverride {
		if !config.ValidWindowsTerminalWindow(options["--window"]) {
			return invalid("invalid Windows Terminal window %q", options["--window"])
		}
		profile.WindowsTerminalWindow = options["--window"]
	}
	_, modeOverride := options["--terminal-mode"]
	if modeOverride {
		if !config.ValidWindowsTerminalMode(options["--terminal-mode"]) {
			return invalid("invalid Windows Terminal mode %q", options["--terminal-mode"])
		}
		profile.WindowsTerminalMode = options["--terminal-mode"]
	}
	executor := &backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	environment := backend.CurrentEnvironment()
	registry := backend.NewRegistry(environment,
		shelladapter.New(executor, shelladapter.Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}),
		tmuxadapter.New(executor, os.Getenv),
		cmuxadapter.New(executor, runtime.GOOS),
		wtadapter.New(executor, wtadapter.Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}),
	)
	request := backend.OpenRequest{Project: project, Profile: profile}
	selection, selectErr := registry.Select(context.Background(), request, requested)
	if selectErr != nil {
		var unavailable *backend.UnavailableError
		if errors.As(selectErr, &unavailable) {
			fallbacks := make([]string, len(unavailable.Fallback))
			for index, fallback := range unavailable.Fallback {
				fallbacks[index] = string(fallback)
			}
			return &commandError{
				ExitCode: ExitUnavailable, Code: "BACKEND_UNAVAILABLE", Message: unavailable.Error(),
				Details: map[string]any{"backend": unavailable.Backend, "fallbacks": fallbacks},
			}
		}
		return invalid("%s", selectErr)
	}
	if (windowOverride || modeOverride) && selection.Adapter.Name() != backend.WindowsTerminal {
		return invalid("--window and --terminal-mode require the Windows Terminal backend")
	}
	for _, warning := range selection.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	result, openErr := selection.Adapter.OpenProject(context.Background(), request)
	if result.Stdout != "" {
		fmt.Fprint(stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(stderr, result.Stderr)
	}
	if openErr != nil {
		return &commandError{
			ExitCode: ExitGeneral, Code: "BACKEND_EXECUTION_FAILED",
			Message: fmt.Sprintf("backend %q failed for %s: %v", result.Backend, result.Reference, openErr),
			Details: map[string]any{"backend": result.Backend, "reference": result.Reference, "exit_code": result.ExitCode, "command": result.Command},
		}
	}
	fmt.Fprintf(stdout, "opened %s with %s (%s)\n", project.ID, result.Backend, result.Reference)
	return nil
}

func runProjects(args []string, paths config.Paths, stdout io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("projects subcommand is required")
	}
	store := projects.NewStore(paths)
	switch args[0] {
	case "list":
		jsonMode, err := onlyJSONFlag(args[1:])
		if err != nil {
			return err
		}
		items, loadErr := store.List()
		if loadErr != nil {
			return configError(loadErr)
		}
		if jsonMode {
			if err := output.Write(stdout, map[string]any{"projects": items}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, project := range items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", project.ID, project.Profile, project.Path)
		}
		return nil
	case "show":
		positionals, options, err := parseOptions(args[1:], map[string]bool{"--json": false})
		if err != nil || len(positionals) != 1 {
			return invalid("usage: wb projects show <id> [--json]")
		}
		project, found, loadErr := store.Show(positionals[0])
		if loadErr != nil {
			return configError(loadErr)
		}
		if !found {
			return &commandError{ExitCode: ExitGeneral, Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q was not found", positionals[0]), Details: map[string]any{"project_id": positionals[0]}}
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"project": project}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "id: %s\nname: %s\npath: %s\nrepo_root: %s\nprofile: %s\ndefault_backend: %s\neditor: %s\n", project.ID, project.Name, project.Path, project.RepoRoot, project.Profile, project.DefaultBackend, project.Editor)
		return nil
	case "add":
		positionals, options, err := parseOptions(args[1:], map[string]bool{"--id": true, "--profile": true})
		if err != nil || len(positionals) != 1 {
			return invalid("usage: wb projects add <path> [--id <id>] [--profile <profile>]")
		}
		project, backup, addErr := store.Add(positionals[0], options["--id"], options["--profile"])
		if addErr != nil {
			if isConflict(addErr) {
				return &commandError{ExitCode: ExitConflict, Code: "PROJECT_CONFLICT", Message: addErr.Error()}
			}
			return configError(addErr)
		}
		fmt.Fprintf(stdout, "added %s\t%s\n", project.ID, project.Path)
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "remove":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return invalid("usage: wb projects remove <id>")
		}
		project, found, backup, removeErr := store.Remove(args[1])
		if removeErr != nil {
			return configError(removeErr)
		}
		if !found {
			return &commandError{ExitCode: ExitGeneral, Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q was not found", args[1])}
		}
		fmt.Fprintf(stdout, "removed %s from registry; repository preserved at %s\n", project.ID, project.Path)
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	default:
		return invalid("unknown projects subcommand %q", args[0])
	}
}

func runEnv(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("env subcommand is required")
	}
	store := environments.NewStore(paths)
	switch args[0] {
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 0 {
			return invalid("usage: wb env list [--json]")
		}
		items, err := store.List()
		if err != nil {
			return configError(err)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"environments": items}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.AWSProfile, item.AWSRegion, item.KubeContext, item.KubeNamespace)
		}
		return nil
	case "show":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb env show <id> [--json]")
		}
		item, found, err := store.Show(positionals[0])
		if err != nil {
			return configError(err)
		}
		if !found {
			return envNotFound(positionals[0])
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"environment": item}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "id: %s\naws_profile: %s\naws_region: %s\nkube_context: %s\nkube_namespace: %s\n", item.ID, item.AWSProfile, item.AWSRegion, item.KubeContext, item.KubeNamespace)
		keys := make([]string, 0, len(item.Exports))
		for key := range item.Exports {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(stdout, "export: %s=%s\n", key, item.Exports[key])
		}
		secretKeys := make([]string, 0, len(item.Secrets))
		for key := range item.Secrets {
			secretKeys = append(secretKeys, key)
		}
		sort.Strings(secretKeys)
		for _, key := range secretKeys {
			fmt.Fprintf(stdout, "secret: %s=%s\n", key, item.Secrets[key])
		}
		return nil
	case "add":
		item, jsonMode, parseErr := parseEnvAdd(args[1:])
		if parseErr != nil {
			return parseErr
		}
		backup, err := store.Add(item)
		if err != nil {
			return envError(err)
		}
		if jsonMode {
			if err := output.Write(stdout, map[string]any{"environment": item, "backup": backup}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "added %s\n", item.ID)
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "remove":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb env remove <id> [--json]")
		}
		item, found, backup, err := store.Remove(positionals[0])
		if err != nil {
			return envError(err)
		}
		if !found {
			return envNotFound(positionals[0])
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"environment": item, "backup": backup}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "removed %s\n", item.ID)
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "export":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false, "--resolve-secrets": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb env export <id> [--resolve-secrets] [--json]")
		}
		_, jsonMode := options["--json"]
		_, resolveSecrets := options["--resolve-secrets"]
		if optionErr := validateEnvExportOptions(jsonMode, resolveSecrets, isTerminalWriter(stdout)); optionErr != nil {
			return optionErr
		}
		item, found, err := store.Show(positionals[0])
		if err != nil {
			return configError(err)
		}
		if !found {
			return envNotFound(positionals[0])
		}
		pending := []string{}
		warnings := []string{}
		if item.KubeContext != "" {
			pending = append(pending, "kube_context")
		}
		if item.KubeNamespace != "" {
			pending = append(pending, "kube_namespace")
		}
		if len(pending) > 0 {
			warnings = append(warnings, "kube context/namespace mutation is not implemented; wb env export emits environment variables only")
		}
		secretStatuses := environments.PendingSecretReferences(item)
		if len(secretStatuses) > 0 && !resolveSecrets {
			warnings = append(warnings, "secret references were not resolved; use --resolve-secrets with command substitution or a pipe")
		}
		if jsonMode {
			if err := output.Write(stdout, map[string]any{"environment": item, "exports": environments.ExportValues(item), "secret_references": secretStatuses, "pending_mutations": pending}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		resolved := map[string][]byte{}
		if resolveSecrets {
			var resolveErr error
			resolved, secretStatuses, resolveErr = environments.ResolveSecretReferences(item, secrets.NewStore(paths))
			if resolveErr != nil {
				return &commandError{ExitCode: ExitUnavailable, Code: "ENVIRONMENT_SECRETS_UNAVAILABLE", Message: resolveErr.Error(), Details: map[string]any{"secret_references": secretStatuses}}
			}
			defer environments.ZeroResolvedSecrets(resolved)
		}
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
		if err := environments.WriteShellExports(stdout, item, resolved); err != nil {
			return generalError(err)
		}
		return nil
	case "health":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb env health <id> [--json]")
		}
		item, found, err := store.Show(positionals[0])
		if err != nil {
			return configError(err)
		}
		if !found {
			return envNotFound(positionals[0])
		}
		statuses := environments.CheckSecretReferences(item, secrets.NewStore(paths))
		available := true
		for _, status := range statuses {
			available = available && status.Available
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"environment_id": item.ID, "available": available, "secret_references": statuses}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "environment: %s\navailable: %t\n", item.ID, available)
		for _, status := range statuses {
			state := "available"
			if !status.Available {
				state = status.Reason
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", state, status.Variable, status.Reference)
		}
		return nil
	case "migrate":
		return runEnvMigrate(args[1:], paths, stdout, stderr)
	default:
		return invalid("unknown env subcommand %q", args[0])
	}
}

func parseEnvAdd(args []string) (environments.Environment, bool, *commandError) {
	item := environments.Environment{Exports: map[string]string{}, Secrets: map[string]string{}}
	jsonMode := false
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--json" {
			if jsonMode {
				return item, false, invalid("option %q was provided more than once", argument)
			}
			jsonMode = true
			continue
		}
		if !strings.HasPrefix(argument, "--") {
			positionals = append(positionals, argument)
			continue
		}
		if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
			return item, false, invalid("option %q requires a value", argument)
		}
		index++
		value := args[index]
		switch argument {
		case "--aws-profile":
			if item.AWSProfile != "" {
				return item, false, invalid("option %q was provided more than once", argument)
			}
			item.AWSProfile = value
		case "--aws-region":
			if item.AWSRegion != "" {
				return item, false, invalid("option %q was provided more than once", argument)
			}
			item.AWSRegion = value
		case "--kube-context":
			if item.KubeContext != "" {
				return item, false, invalid("option %q was provided more than once", argument)
			}
			item.KubeContext = value
		case "--kube-namespace":
			if item.KubeNamespace != "" {
				return item, false, invalid("option %q was provided more than once", argument)
			}
			item.KubeNamespace = value
		case "--set":
			key, exportValue, found := strings.Cut(value, "=")
			if !found || !environments.ValidVariableName(key) || environments.ReservedKey(key) {
				return item, false, invalid("--set requires a non-reserved KEY=VALUE")
			}
			if _, exists := item.Exports[key]; exists {
				return item, false, invalid("duplicate --set key %q", key)
			}
			if _, exists := item.Secrets[key]; exists {
				return item, false, invalid("variable %q cannot be provided by both --set and --secret", key)
			}
			item.Exports[key] = exportValue
		case "--secret":
			key, reference, found := strings.Cut(value, "=")
			if !found || !environments.ValidVariableName(key) || environments.ReservedKey(key) {
				return item, false, invalid("--secret requires a non-reserved KEY=sec://service/field")
			}
			if _, exists := item.Secrets[key]; exists {
				return item, false, invalid("duplicate --secret key %q", key)
			}
			if _, exists := item.Exports[key]; exists {
				return item, false, invalid("variable %q cannot be provided by both --set and --secret", key)
			}
			if _, err := environments.ParseSecretReference(reference); err != nil {
				return item, false, invalid("invalid --secret reference for %q: %s", key, err)
			}
			item.Secrets[key] = reference
		default:
			return item, false, invalid("unknown option %q", argument)
		}
	}
	if len(positionals) != 1 {
		return item, false, invalid("usage: wb env add <id> [--aws-profile <value>] [--aws-region <value>] [--kube-context <value>] [--kube-namespace <value>] [--set KEY=VALUE]... [--secret KEY=sec://service/field]... [--json]")
	}
	item.ID = positionals[0]
	if err := environments.ValidateRegistry(environments.Registry{SchemaVersion: environments.SchemaVersion, Environments: []environments.Environment{item}}); err != nil {
		return item, false, invalid("%s", err)
	}
	return item, jsonMode, nil
}

func runEnvMigrate(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 || args[0] != "check" && args[0] != "apply" {
		return invalid("usage: wb env migrate check|apply [--source <dir>] [--json]")
	}
	mode := args[0]
	positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--source": true, "--json": false})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb env migrate check|apply [--source <dir>] [--json]")
	}
	source := options["--source"]
	if source == "" {
		source = defaultWenvDir(paths)
	}
	store := environments.NewStore(paths)
	plan, err := environments.PlanWenv(source, store)
	if err != nil {
		return configError(err)
	}
	jsonMode := false
	if _, jsonMode = options["--json"]; jsonMode {
		if mode == "check" {
			if err := output.Write(stdout, map[string]any{"migration": plan, "applied": false}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
	}
	if !jsonMode {
		fmt.Fprintf(stdout, "wenv source: %s\nready: %d\nexisting: %d\nblocked: %d\n", plan.SourceDir, plan.Ready, plan.Existing, plan.Blocked)
		for _, item := range plan.Items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.Status, item.ID, strings.Join(item.Issues, "; "))
		}
		if mode == "check" {
			fmt.Fprintln(stdout, "dry-run only; no files changed")
			return nil
		}
	}
	if !plan.CanApply {
		return &commandError{ExitCode: ExitConflict, Code: "ENV_MIGRATION_BLOCKED", Message: "wenv migration is blocked; no environments were changed", Details: map[string]any{"migration": plan}}
	}
	backup, applyErr := environments.ApplyWenv(plan, store)
	if applyErr != nil {
		return envError(applyErr)
	}
	if jsonMode {
		if err := output.Write(stdout, map[string]any{"migration": plan, "applied": true, "backup": backup}, nil); err != nil {
			return generalError(err)
		}
		return nil
	}
	fmt.Fprintf(stdout, "applied %d environment(s)\n", plan.Ready)
	if backup != "" {
		fmt.Fprintf(stdout, "backup %s\n", backup)
	}
	return nil
}

func defaultWenvDir(paths config.Paths) string {
	if override := os.Getenv("BINBOX_WENV_DIR"); override != "" {
		return override
	}
	return filepath.Join(filepath.Dir(paths.ConfigDir), "binbox", "wenv.d")
}

func envNotFound(id string) *commandError {
	return &commandError{ExitCode: ExitGeneral, Code: "ENVIRONMENT_NOT_FOUND", Message: fmt.Sprintf("environment %q was not found", id), Details: map[string]any{"environment_id": id}}
}

func envError(err error) *commandError {
	var invalidErr *environments.InvalidError
	if errors.As(err, &invalidErr) {
		return invalid("%s", err)
	}
	var conflictErr *environments.ConflictError
	if errors.As(err, &conflictErr) {
		return &commandError{ExitCode: ExitConflict, Code: "ENVIRONMENT_CONFLICT", Message: err.Error()}
	}
	return configError(err)
}

func runConfig(args []string, paths config.Paths, stdout io.Writer) *commandError {
	if len(args) != 1 || args[0] != "validate" {
		return invalid("usage: wb config validate")
	}
	if err := config.Validate(paths); err != nil {
		return configError(err)
	}
	if _, err := projects.NewStore(paths).Load(); err != nil {
		return configError(err)
	}
	if _, err := environments.NewStore(paths).Load(); err != nil {
		return configError(err)
	}
	if err := secrets.NewStore(paths).Validate(); err != nil {
		return configError(err)
	}
	fmt.Fprintf(stdout, "configuration valid\nconfig: %s\nprojects: %s\nenvironments: %s\nsecrets: %s\n", paths.ConfigFile, paths.ProjectsFile, paths.EnvironmentsFile, paths.SecretsFile)
	return nil
}

func runSecrets(args []string, paths config.Paths, stdin io.Reader, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("secrets subcommand is required")
	}
	store := secrets.NewStore(paths)
	switch args[0] {
	case "init":
		jsonMode, err := exactJSONOption(args[1:], "usage: wb secrets init [--json]")
		if err != nil {
			return err
		}
		if initErr := store.Init(); initErr != nil {
			return secretError(initErr)
		}
		metadata := map[string]any{"initialized": true, "identity_path": paths.AgeIdentityFile, "store_path": paths.SecretsFile}
		if jsonMode {
			if err := output.Write(stdout, metadata, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "initialized Workbench secrets\nidentity: %s\nstore: %s\n", paths.AgeIdentityFile, paths.SecretsFile)
		return nil
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) > 1 {
			return invalid("usage: wb secrets list [service] [--json]")
		}
		service := ""
		if len(positionals) == 1 {
			service = positionals[0]
		}
		entries, listErr := store.List(service)
		if listErr != nil {
			return secretError(listErr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"entries": entries, "service": service}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		if service == "" {
			seen := map[string]bool{}
			for _, entry := range entries {
				if !seen[entry.Service] {
					fmt.Fprintln(stdout, entry.Service)
					seen[entry.Service] = true
				}
			}
		} else {
			for _, entry := range entries {
				fmt.Fprintln(stdout, entry.Field)
			}
		}
		return nil
	case "set":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false, "--replace": false})
		if parseErr != nil || len(positionals) != 2 {
			return invalid("usage: wb secrets set <service> <field> [--replace] [--json]")
		}
		value, inputErr := readSecretInput(stdin, stderr, positionals[0]+"/"+positionals[1])
		if inputErr != nil {
			return secretError(inputErr)
		}
		defer zeroBytes(value)
		_, replace := options["--replace"]
		backup, setErr := store.Set(positionals[0], positionals[1], value, replace)
		if setErr != nil {
			return secretError(setErr)
		}
		metadata := map[string]any{"service": positionals[0], "field": positionals[1], "stored": true, "replace_requested": replace, "backup": backup}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, metadata, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "stored %s/%s\n", positionals[0], positionals[1])
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "get":
		for _, arg := range args[1:] {
			if arg == "--json" {
				return invalid("wb secrets get does not support --json because stdout is reserved for plaintext")
			}
		}
		if len(args) < 2 || len(args) > 3 {
			return invalid("usage: wb secrets get <service> [field]")
		}
		field := ""
		if len(args) == 3 {
			field = args[2]
		}
		value, _, getErr := store.Get(args[1], field)
		if getErr != nil {
			return secretError(getErr)
		}
		defer zeroBytes(value)
		if written, err := stdout.Write(value); err != nil {
			return generalError(err)
		} else if written != len(value) {
			return generalError(io.ErrShortWrite)
		}
		if len(value) == 0 || value[len(value)-1] != '\n' {
			_, _ = fmt.Fprintln(stdout)
		}
		return nil
	case "remove", "rm":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false, "--yes": false})
		if parseErr != nil || len(positionals) < 1 || len(positionals) > 2 {
			return invalid("usage: wb secrets remove <service> [field] [--yes] [--json]")
		}
		field := ""
		if len(positionals) == 2 {
			field = positionals[1]
		}
		target := positionals[0]
		if field != "" {
			target += "/" + field
		}
		_, assumeYes := options["--yes"]
		confirmed, confirmErr := confirmSecretRemoval(stdin, stderr, target, assumeYes, isTerminalReader(stdin))
		if confirmErr != nil {
			return invalid("%s", confirmErr)
		}
		if !confirmed {
			if _, jsonMode := options["--json"]; jsonMode {
				if err := output.Write(stdout, map[string]any{"service": positionals[0], "field": field, "removed": false}, nil); err != nil {
					return generalError(err)
				}
			} else {
				fmt.Fprintln(stdout, "removal cancelled")
			}
			return nil
		}
		backup, removeErr := store.Remove(positionals[0], field)
		if removeErr != nil {
			return secretError(removeErr)
		}
		metadata := map[string]any{"service": positionals[0], "field": field, "removed": true, "backup": backup}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, metadata, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "removed %s\n", target)
		if backup != "" {
			fmt.Fprintf(stdout, "backup %s\n", backup)
		}
		return nil
	case "migrate":
		return runSecretsMigrate(args[1:], paths, stdout, stderr)
	default:
		return invalid("unknown secrets subcommand %q", args[0])
	}
}

func runSecretsMigrate(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 || args[0] != "check" && args[0] != "apply" {
		return invalid("usage: wb secrets migrate check|apply [--json]")
	}
	mode := args[0]
	jsonMode, parseErr := exactJSONOption(args[1:], "usage: wb secrets migrate check|apply [--json]")
	if parseErr != nil {
		return parseErr
	}
	plan, err := secrets.PlanMigration(secrets.DefaultLegacyPaths(paths), paths)
	if err != nil {
		return secretError(err)
	}
	if mode == "apply" && !plan.CanApply {
		return &commandError{ExitCode: ExitConflict, Code: "SECRETS_MIGRATION_BLOCKED", Message: "legacy secrets migration is blocked; no destination files were changed", Details: map[string]any{"migration": plan}}
	}
	if mode == "apply" {
		if err := secrets.ApplyMigration(plan, paths); err != nil {
			return secretError(err)
		}
	}
	warnings := []string{}
	if mode == "apply" {
		warnings = append(warnings, "legacy identity and store were retained; two decryptable copies exist until you deliberately retire one")
	}
	if jsonMode {
		if err := output.Write(stdout, map[string]any{"migration": plan, "applied": mode == "apply"}, warnings); err != nil {
			return generalError(err)
		}
		return nil
	}
	fmt.Fprintf(stdout, "legacy identity: %s\nlegacy store: %s\nidentity: %s mode=%s healthy=%t\nstore: mode=%s healthy=%t\ndecrypt: %t schema: %t names: %t\nservices: %d fields: %d\ndestination available: %t\ncan apply: %t\n", plan.SourceIdentity, plan.SourceStore, plan.IdentityType, plan.IdentityMode, plan.IdentityHealthy, plan.StoreMode, plan.StoreHealthy, plan.DecryptValid, plan.SchemaValid, plan.NamesValid, plan.ServiceCount, plan.FieldCount, plan.DestinationAvailable, plan.CanApply)
	for _, issue := range plan.Issues {
		fmt.Fprintf(stdout, "issue: %s\n", issue)
	}
	if mode == "check" {
		fmt.Fprintln(stdout, "dry-run only; no files changed")
	} else {
		fmt.Fprintln(stdout, "migration applied; legacy files were not deleted")
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	return nil
}

func exactJSONOption(args []string, usage string) (bool, *commandError) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "--json" {
		return true, nil
	}
	return false, invalid("%s", usage)
}

func readSecretInput(stdin io.Reader, stderr io.Writer, label string) ([]byte, error) {
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprintf(stderr, "%s value: ", label)
		value, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return nil, fmt.Errorf("read hidden secret value: %w", err)
		}
		if len(value) == 0 {
			return nil, &secrets.InvalidError{Message: "secret value must not be empty"}
		}
		return value, nil
	}
	value, err := io.ReadAll(io.LimitReader(stdin, (16<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read secret value from stdin: %w", err)
	}
	if len(value) > 16<<20 {
		return nil, &secrets.InvalidError{Message: "secret value is too large"}
	}
	if len(value) == 0 {
		return nil, &secrets.InvalidError{Message: "secret value must not be empty"}
	}
	return value, nil
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func isTerminalReader(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func validateEnvExportOptions(jsonMode, resolveSecrets, outputIsTerminal bool) *commandError {
	if jsonMode && resolveSecrets {
		return invalid("--resolve-secrets cannot be combined with --json")
	}
	if resolveSecrets && outputIsTerminal {
		return invalid("--resolve-secrets refuses to write secret values to a terminal; use command substitution or a pipe")
	}
	return nil
}

func confirmSecretRemoval(stdin io.Reader, stderr io.Writer, target string, assumeYes, interactive bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	if !interactive {
		return false, errors.New("non-interactive secret removal requires --yes")
	}
	fmt.Fprintf(stderr, "remove secret %s? [y/N] ", target)
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read removal confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func secretError(err error) *commandError {
	var invalidErr *secrets.InvalidError
	if errors.As(err, &invalidErr) {
		return invalid("%s", err)
	}
	var conflictErr *secrets.ConflictError
	if errors.As(err, &conflictErr) {
		return &commandError{ExitCode: ExitConflict, Code: "SECRET_CONFLICT", Message: err.Error()}
	}
	var notFoundErr *secrets.NotFoundError
	if errors.As(err, &notFoundErr) {
		return &commandError{ExitCode: ExitUnavailable, Code: "SECRET_NOT_FOUND", Message: err.Error()}
	}
	return generalError(err)
}

func runMigrate(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) > 0 && args[0] == "sessionizer" {
		args = args[1:]
	}
	positionals, options, err := parseOptions(args, map[string]bool{"--check": false, "--apply": false, "--file": true, "--profile": true})
	if err != nil || len(positionals) != 0 {
		return invalid("usage: wb migrate [sessionizer] --check|--apply [--file <path>] [--profile <profile>]")
	}
	_, check := options["--check"]
	_, apply := options["--apply"]
	if check == apply {
		return invalid("exactly one of --check or --apply is required")
	}
	source := options["--file"]
	if source == "" {
		source = filepath.Join(filepath.Dir(paths.ConfigDir), "tmux-sessionizer", "dirs")
	}
	store := projects.NewStore(paths)
	plan, planErr := migrate.PlanSessionizer(source, options["--profile"], store)
	if planErr != nil {
		return configError(planErr)
	}
	fmt.Fprintf(stdout, "sessionizer source: %s\n", plan.Source)
	fmt.Fprintf(stdout, "projects to add: %d\n", len(plan.Projects))
	for _, project := range plan.Projects {
		fmt.Fprintf(stdout, "+ %s\t%s\n", project.ID, project.Path)
	}
	for _, skipped := range plan.Skipped {
		fmt.Fprintf(stdout, "= %s\n", skipped)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	if check {
		fmt.Fprintln(stdout, "dry-run only; no files changed")
		return nil
	}
	backups, applyErr := migrate.ApplySessionizer(plan, store)
	if applyErr != nil {
		return generalError(applyErr)
	}
	fmt.Fprintf(stdout, "applied %d project(s)\n", len(plan.Projects))
	for _, backup := range backups {
		fmt.Fprintf(stdout, "backup %s\n", backup)
	}
	return nil
}

func parseOptions(args []string, specification map[string]bool) ([]string, map[string]string, *commandError) {
	positionals := []string{}
	options := map[string]string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		requiresValue, known := specification[argument]
		if !known {
			if strings.HasPrefix(argument, "-") {
				return nil, nil, invalid("unknown option %q", argument)
			}
			positionals = append(positionals, argument)
			continue
		}
		if _, duplicate := options[argument]; duplicate {
			return nil, nil, invalid("option %q was provided more than once", argument)
		}
		if requiresValue {
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return nil, nil, invalid("option %q requires a value", argument)
			}
			options[argument] = args[index]
		} else {
			options[argument] = "true"
		}
	}
	return positionals, options, nil
}

func onlyJSONFlag(args []string) (bool, *commandError) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "--json" {
		return true, nil
	}
	return false, invalid("usage: wb projects list [--json]")
}

func invalid(format string, values ...any) *commandError {
	return &commandError{ExitCode: ExitArgument, Code: "INVALID_ARGUMENT", Message: fmt.Sprintf(format, values...)}
}

func configError(err error) *commandError {
	return &commandError{ExitCode: ExitArgument, Code: "CONFIG_INVALID", Message: err.Error()}
}

func generalError(err error) *commandError {
	return &commandError{ExitCode: ExitGeneral, Code: "OPERATION_FAILED", Message: err.Error()}
}

func report(stdout, stderr io.Writer, jsonMode bool, err *commandError) int {
	if err.Reported {
		return err.ExitCode
	}
	if jsonMode {
		_ = output.WriteError(stdout, err.Code, err.Message, err.Details)
	} else {
		fmt.Fprintf(stderr, "wb: %s\n", err.Message)
	}
	return err.ExitCode
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isConflict(err error) bool {
	message := err.Error()
	return strings.Contains(message, "duplicate project id") || strings.Contains(message, "already owned")
}

func usage() string {
	return `wb - local workbench control plane

Usage:
  wb projects list [--json]
  wb projects show <id> [--json]
  wb projects add <path> [--id <id>] [--profile <profile>]
  wb projects remove <id>
  wb env list [--json]
  wb env show <id> [--json]
  wb env add <id> [--aws-profile <value>] [--aws-region <value>] [--kube-context <value>] [--kube-namespace <value>] [--set KEY=VALUE]... [--secret KEY=sec://service/field]... [--json]
  wb env remove <id> [--json]
  wb env health <id> [--json]
  wb env export <id> [--resolve-secrets] [--json]
  wb env migrate check|apply [--source <wenv.d>] [--json]
  wb secrets init [--json]
  wb secrets list [service] [--json]
  wb secrets set <service> <field> [--replace] [--json]
  wb secrets get <service> [field]
  wb secrets remove <service> [field] [--yes] [--json]
  wb secrets migrate check|apply [--json]
  wb open <project-id> [--backend <backend>] [--window <last|new|id>] [--terminal-mode <tab|split-auto|split-horizontal|split-vertical>]
  wb worktrees list <project-id> [--json]
  wb worktrees create <project-id> <branch> [--base <ref>]
  wb worktrees remove <worktree-id> [--delete-branch]
  wb agents list [--project <id>] [--json]
  wb agents show <task-id> [--json]
  wb agents start <project-id> --agent codex|claude [--worktree <id>] [--backend <backend>]
  wb agents jump <task-id>
  wb agents stop <task-id>
  wb tasks list [--project <id>] [--json]
  wb tasks show <task-id> [--json]
  wb tasks jump <task-id>
  wb tasks stop <task-id>
  wb sessions list [--json]
  wb sessions jump <pane-id>
  wb overview [--json]
  wb workflows catalog [--project <id>] [--json]
  wb workflows run <workflow-id> --project <id> [--json]
  wb workflows history [--project <id>] [--json]
  wb workflows show <run-id> [--json]
  wb compatibility observe --client <client> --feature <feature> --source <source>
  wb doctor [--profile <name>] [--json] [--strict]
  wb dashboard [--open auto|cmux|browser|none] [--port <0-65535>]
  wb config validate
  wb migrate [sessionizer] --check|--apply [--file <path>] [--profile <profile>]
`
}

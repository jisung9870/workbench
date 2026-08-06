package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
	"github.com/jisung9870/workbench/internal/migrate"
	"github.com/jisung9870/workbench/internal/output"
	"github.com/jisung9870/workbench/internal/projects"
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
	if handled, code := handleHelp(args, stdout, stderr); handled {
		return code
	}
	if handled, code := handleCompletion(args, stdout, stderr); handled {
		return code
	}
	jsonMode := contains(args, "--json")
	paths, err := config.ResolvePaths()
	if err != nil {
		return report(stdout, stderr, jsonMode, &commandError{ExitCode: ExitArgument, Code: "CONFIG_INVALID", Message: err.Error()})
	}
	var commandErr *commandError
	switch args[0] {
	case "projects":
		commandErr = runProjects(args[1:], paths, stdout)
	case "config":
		commandErr = runConfig(args[1:], paths, stdout)
	case "migrate":
		commandErr = runMigrate(args[1:], paths, stdout, stderr)
	case "sessions":
		commandErr = runSessions(args[1:], paths, stdout, stderr)
	case "open":
		commandErr = runOpen(args[1:], paths, stdout, stderr)
	case "worktrees":
		commandErr = runWorktrees(args[1:], paths, stdout, stderr)
	case "agents":
		commandErr = runAgents(args[1:], paths, stdout, stderr)
	case "compatibility":
		commandErr = runCompatibility(args[1:], paths, stdout)
	case "doctor":
		commandErr = runDoctor(args[1:], paths, stdout, stderr)
	case "dashboard":
		commandErr = runDashboard(args[1:], paths, stdout, stderr)
	case "server":
		commandErr = runServer(args[1:], paths, stdout, stderr)
	default:
		commandErr = invalid("unknown command %q", args[0])
	}
	if commandErr != nil {
		return report(stdout, stderr, jsonMode, commandErr)
	}
	return ExitOK
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
	fmt.Fprintf(stdout, "configuration valid\nconfig: %s\nprojects: %s\n", paths.ConfigFile, paths.ProjectsFile)
	return nil
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

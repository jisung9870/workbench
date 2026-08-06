package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/output"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/sessions"
)

func runSessions(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("sessions subcommand is required")
	}
	manager := sessions.NewManager(&backend.OSExecutor{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}, os.Getenv)
	ctx := context.Background()
	switch args[0] {
	case "list":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 0 {
			return invalid("usage: wb sessions list [--json]")
		}
		items, warnings, err := manager.List(ctx)
		if err != nil {
			return sessionError(err)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"sessions": items}, warnings); err != nil {
				return generalError(err)
			}
			return nil
		}
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
		for _, item := range items {
			projectID := item.ProjectID
			if projectID == "" {
				projectID = "-"
			}
			path := item.ProjectPath
			if path == "" {
				path = item.StartPath
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\t%d\t%s\n", item.Name, item.Ownership, projectID, item.Attached, item.Windows, path)
		}
		return nil
	case "show":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb sessions show <session-name> [--json]")
		}
		item, err := manager.Show(ctx, positionals[0])
		if err != nil {
			return sessionError(err)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"session": item}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		writeSession(stdout, item)
		return nil
	case "jump":
		if len(args) != 2 || args[1] == "" || args[1][0] == '-' {
			return invalid("usage: wb sessions jump <session-name>")
		}
		item, err := manager.Jump(ctx, args[1])
		if err != nil {
			return sessionError(err)
		}
		fmt.Fprintf(stdout, "jumped to tmux session %s (%s)\n", item.Name, item.Ownership)
		return nil
	case "adopt":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb sessions adopt <project-id> [--json]")
		}
		project, err := sessionProject(paths, positionals[0])
		if err != nil {
			return err
		}
		item, changed, adoptErr := manager.Adopt(ctx, project)
		if adoptErr != nil {
			return sessionError(adoptErr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"session": item, "adopted": changed}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		if changed {
			fmt.Fprintf(stdout, "adopted tmux session %s for project %s\n", item.Name, project.ID)
		} else {
			fmt.Fprintf(stdout, "tmux session %s is already managed\n", item.Name)
		}
		return nil
	case "stop":
		positionals, options, parseErr := parseOptions(args[1:], map[string]bool{"--json": false})
		if parseErr != nil || len(positionals) != 1 {
			return invalid("usage: wb sessions stop <project-id> [--json]")
		}
		project, err := sessionProject(paths, positionals[0])
		if err != nil {
			return err
		}
		item, stopErr := manager.Stop(ctx, project)
		if stopErr != nil {
			return sessionError(stopErr)
		}
		if _, jsonMode := options["--json"]; jsonMode {
			if err := output.Write(stdout, map[string]any{"session": item, "stopped": true}, nil); err != nil {
				return generalError(err)
			}
			return nil
		}
		fmt.Fprintf(stdout, "stopped managed tmux session %s\n", item.Name)
		return nil
	default:
		return invalid("unknown sessions subcommand %q", args[0])
	}
}

func sessionProject(paths config.Paths, projectID string) (projects.Project, *commandError) {
	project, found, err := projects.NewStore(paths).Show(projectID)
	if err != nil {
		return projects.Project{}, configError(err)
	}
	if !found {
		return projects.Project{}, &commandError{
			ExitCode: ExitGeneral, Code: "PROJECT_NOT_FOUND",
			Message: fmt.Sprintf("project %q was not found", projectID),
			Details: map[string]any{"project_id": projectID},
		}
	}
	return project, nil
}

func sessionError(err error) *commandError {
	var unavailable *backend.UnavailableError
	if errors.As(err, &unavailable) {
		return &commandError{ExitCode: ExitUnavailable, Code: "BACKEND_UNAVAILABLE", Message: err.Error()}
	}
	var notFound *sessions.NotFoundError
	if errors.As(err, &notFound) {
		return &commandError{
			ExitCode: ExitGeneral, Code: "SESSION_NOT_FOUND", Message: err.Error(),
			Details: map[string]any{"session": notFound.Name},
		}
	}
	var conflict *sessions.ConflictError
	if errors.As(err, &conflict) {
		return &commandError{ExitCode: ExitConflict, Code: "SESSION_CONFLICT", Message: err.Error()}
	}
	return generalError(err)
}

func writeSession(stdout io.Writer, item sessions.Item) {
	fmt.Fprintf(stdout, "name: %s\nownership: %s\nmanaged: %t\nproject_id: %s\nproject_path: %s\nstart_path: %s\nattached_clients: %d\nwindows: %d\n",
		item.Name, item.Ownership, item.Managed, item.ProjectID, item.ProjectPath, item.StartPath, item.Attached, item.Windows)
}

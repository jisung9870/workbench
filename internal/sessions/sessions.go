package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

const (
	ManagedOption     = "@workbench_managed"
	ProjectIDOption   = "@workbench_project_id"
	ProjectPathOption = "@workbench_project_path"
)

type Ownership string

const (
	Managed Ownership = "managed"
	Legacy  Ownership = "legacy"
	Foreign Ownership = "foreign"
)

type Item struct {
	Name        string    `json:"name"`
	Ownership   Ownership `json:"ownership"`
	Managed     bool      `json:"managed"`
	ProjectID   string    `json:"project_id,omitempty"`
	ProjectPath string    `json:"project_path,omitempty"`
	StartPath   string    `json:"start_path,omitempty"`
	Attached    int       `json:"attached_clients"`
	Windows     int       `json:"windows"`
}

type NotFoundError struct {
	Name string
}

func (err *NotFoundError) Error() string {
	return fmt.Sprintf("tmux session %q was not found", err.Name)
}

type ConflictError struct {
	Message string
}

func (err *ConflictError) Error() string { return err.Message }

type Manager struct {
	executor backend.Executor
	getenv   func(string) string
}

func NewManager(executor backend.Executor, getenv func(string) string) *Manager {
	return &Manager{executor: executor, getenv: getenv}
}

func (manager *Manager) List(ctx context.Context) ([]Item, []string, error) {
	command, err := manager.command()
	if err != nil {
		return nil, nil, err
	}
	result, listErr := manager.executor.Run(ctx, backend.ProcessRequest{
		Name: command,
		Args: []string{"list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{session_windows}"},
	})
	if listErr != nil {
		if result.ExitCode == 1 && strings.TrimSpace(result.Stdout) == "" {
			return []Item{}, []string{}, nil
		}
		return nil, nil, fmt.Errorf("list tmux sessions: %w", listErr)
	}
	items := []Item{}
	warnings := []string{}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || strings.TrimSpace(fields[0]) == "" {
			warnings = append(warnings, fmt.Sprintf("ignored malformed tmux session row %q", line))
			continue
		}
		attached, attachedErr := strconv.Atoi(strings.TrimSpace(fields[1]))
		windows, windowsErr := strconv.Atoi(strings.TrimSpace(fields[2]))
		if attachedErr != nil || windowsErr != nil || attached < 0 || windows < 0 {
			warnings = append(warnings, fmt.Sprintf("ignored tmux session %q with invalid counters", fields[0]))
			continue
		}
		item, itemErr := manager.readItem(ctx, command, strings.TrimSpace(fields[0]), attached, windows)
		if itemErr != nil {
			warnings = append(warnings, fmt.Sprintf("session %s: %v", fields[0], itemErr))
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	sort.Strings(warnings)
	return items, warnings, nil
}

func (manager *Manager) Show(ctx context.Context, name string) (Item, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Item{}, &NotFoundError{Name: name}
	}
	command, err := manager.command()
	if err != nil {
		return Item{}, err
	}
	result, listErr := manager.executor.Run(ctx, backend.ProcessRequest{
		Name: command,
		Args: []string{"list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{session_windows}"},
	})
	if listErr != nil {
		return Item{}, &NotFoundError{Name: name}
	}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || strings.TrimSpace(fields[0]) != name {
			continue
		}
		attached, attachedErr := strconv.Atoi(strings.TrimSpace(fields[1]))
		windows, windowsErr := strconv.Atoi(strings.TrimSpace(fields[2]))
		if attachedErr != nil || windowsErr != nil {
			return Item{}, fmt.Errorf("tmux returned invalid counters for session %q", name)
		}
		return manager.readItem(ctx, command, name, attached, windows)
	}
	return Item{}, &NotFoundError{Name: name}
}

func (manager *Manager) Ensure(ctx context.Context, project projects.Project) (Item, bool, error) {
	path, err := projects.CanonicalPath(project.Path)
	if err != nil {
		return Item{}, false, err
	}
	command, err := manager.command()
	if err != nil {
		return Item{}, false, err
	}
	target := exactTarget(project.ID)
	_, hasErr := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"has-session", "-t", target}})
	if hasErr == nil {
		item, showErr := manager.Show(ctx, project.ID)
		if showErr != nil {
			return Item{}, false, showErr
		}
		if item.Ownership == Foreign {
			return Item{}, false, &ConflictError{Message: fmt.Sprintf("tmux session %q has incomplete Workbench ownership metadata", project.ID)}
		}
		if item.Managed {
			if verifyErr := verifyProject(item, project.ID, path); verifyErr != nil {
				return Item{}, false, verifyErr
			}
		}
		return item, false, nil
	}

	created, createErr := manager.executor.Run(ctx, backend.ProcessRequest{
		Name: command,
		Args: []string{"new-session", "-d", "-s", project.ID, "-c", path},
	})
	if createErr != nil {
		return Item{}, false, fmt.Errorf("create tmux session: %w", createErr)
	}
	if metadataErr := manager.setOwnership(ctx, command, project.ID, path); metadataErr != nil {
		_, cleanupErr := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"kill-session", "-t", target}})
		if cleanupErr != nil {
			return Item{}, false, errors.Join(fmt.Errorf("set tmux session ownership: %w", metadataErr), fmt.Errorf("clean up newly created session: %w", cleanupErr))
		}
		return Item{}, false, fmt.Errorf("set tmux session ownership: %w", metadataErr)
	}
	item, showErr := manager.Show(ctx, project.ID)
	if showErr != nil {
		return Item{}, false, fmt.Errorf("read created tmux session after %v: %w", created.Command, showErr)
	}
	return item, true, nil
}

func (manager *Manager) Adopt(ctx context.Context, project projects.Project) (Item, bool, error) {
	path, err := projects.CanonicalPath(project.Path)
	if err != nil {
		return Item{}, false, err
	}
	item, err := manager.Show(ctx, project.ID)
	if err != nil {
		return Item{}, false, err
	}
	if item.Managed {
		if err := verifyProject(item, project.ID, path); err != nil {
			return Item{}, false, err
		}
		return item, false, nil
	}
	if item.Ownership == Foreign {
		return Item{}, false, &ConflictError{Message: fmt.Sprintf("tmux session %q has incomplete Workbench ownership metadata and cannot be adopted automatically", project.ID)}
	}
	if item.StartPath == "" {
		return Item{}, false, &ConflictError{Message: fmt.Sprintf("tmux session %q has no verifiable start path", project.ID)}
	}
	startPath, err := projects.CanonicalPath(item.StartPath)
	if err != nil {
		return Item{}, false, &ConflictError{Message: fmt.Sprintf("tmux session %q start path cannot be verified: %v", project.ID, err)}
	}
	if filepath.Clean(startPath) != filepath.Clean(path) {
		return Item{}, false, &ConflictError{Message: fmt.Sprintf("tmux session %q starts at %q, not registered project path %q", project.ID, startPath, path)}
	}
	command, err := manager.command()
	if err != nil {
		return Item{}, false, err
	}
	if err := manager.setOwnership(ctx, command, project.ID, path); err != nil {
		return Item{}, false, fmt.Errorf("adopt tmux session: %w", err)
	}
	adopted, err := manager.Show(ctx, project.ID)
	return adopted, true, err
}

func (manager *Manager) Stop(ctx context.Context, project projects.Project) (Item, error) {
	path, err := projects.CanonicalPath(project.Path)
	if err != nil {
		return Item{}, err
	}
	item, err := manager.Show(ctx, project.ID)
	if err != nil {
		return Item{}, err
	}
	if !item.Managed {
		return Item{}, &ConflictError{Message: fmt.Sprintf("tmux session %q is %s; run 'wb sessions adopt %s' before stopping it", project.ID, item.Ownership, project.ID)}
	}
	if err := verifyProject(item, project.ID, path); err != nil {
		return Item{}, err
	}
	command, err := manager.command()
	if err != nil {
		return Item{}, err
	}
	if _, err := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"kill-session", "-t", exactTarget(project.ID)}}); err != nil {
		return Item{}, fmt.Errorf("stop tmux session: %w", err)
	}
	return item, nil
}

func (manager *Manager) Attach(ctx context.Context, name string, allowInteractive bool) (Item, error) {
	item, err := manager.Show(ctx, name)
	if err != nil {
		return Item{}, err
	}
	command, err := manager.command()
	if err != nil {
		return Item{}, err
	}
	args := []string{"attach-session", "-t", exactTarget(name)}
	if manager.getenv != nil && manager.getenv("TMUX") != "" {
		args = []string{"switch-client", "-t", exactTarget(name)}
	} else if !allowInteractive {
		return Item{}, &ConflictError{Message: "session attach requires Workbench to run inside tmux or an interactive CLI terminal"}
	}
	if _, err := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: args, Interactive: true}); err != nil {
		return Item{}, fmt.Errorf("jump to tmux session: %w", err)
	}
	return item, nil
}

func (manager *Manager) readItem(ctx context.Context, command, name string, attached, windows int) (Item, error) {
	target := exactTarget(name)
	managed, err := manager.showOption(ctx, command, target, ManagedOption)
	if err != nil {
		return Item{}, err
	}
	projectID, err := manager.showOption(ctx, command, target, ProjectIDOption)
	if err != nil {
		return Item{}, err
	}
	projectPath, err := manager.showOption(ctx, command, target, ProjectPathOption)
	if err != nil {
		return Item{}, err
	}
	startPath := ""
	panes, panesErr := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"list-panes", "-t", target, "-F", "#{pane_start_path}"}})
	if panesErr == nil {
		for _, line := range strings.Split(panes.Stdout, "\n") {
			if value := strings.TrimSpace(line); value != "" {
				startPath = value
				break
			}
		}
	}
	ownership := Legacy
	isManaged := managed == "1" && projectID != "" && projectPath != ""
	if isManaged {
		ownership = Managed
	} else if managed != "" || projectID != "" || projectPath != "" {
		ownership = Foreign
	}
	return Item{
		Name: name, Ownership: ownership, Managed: isManaged,
		ProjectID: projectID, ProjectPath: projectPath, StartPath: startPath,
		Attached: attached, Windows: windows,
	}, nil
}

func (manager *Manager) showOption(ctx context.Context, command, target, option string) (string, error) {
	result, err := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"show-options", "-qv", "-t", target, option}})
	if err != nil {
		return "", fmt.Errorf("read %s for %s: %w", option, target, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (manager *Manager) setOwnership(ctx context.Context, command, projectID, projectPath string) error {
	target := exactTarget(projectID)
	values := [][2]string{
		{ProjectIDOption, projectID},
		{ProjectPathOption, projectPath},
		{ManagedOption, "1"},
	}
	for _, value := range values {
		if _, err := manager.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"set-option", "-t", target, value[0], value[1]}}); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) command() (string, error) {
	command, err := manager.executor.LookPath("tmux")
	if err != nil {
		return "", &backend.UnavailableError{Backend: backend.Tmux, Reason: "tmux executable was not found"}
	}
	return command, nil
}

func verifyProject(item Item, projectID, projectPath string) error {
	if item.Name != projectID || item.ProjectID != projectID || filepath.Clean(item.ProjectPath) != filepath.Clean(projectPath) {
		return &ConflictError{Message: fmt.Sprintf(
			"tmux session %q ownership does not match project %q at %q", item.Name, projectID, projectPath,
		)}
	}
	return nil
}

func exactTarget(name string) string {
	// The trailing colon forces tmux to parse this as an exact session target.
	// Without it, option and pane commands do not consistently accept =name.
	return "=" + name + ":"
}

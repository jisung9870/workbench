package tmux

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

const snapshotSeparator = "\x1f"

var paneIDPattern = regexp.MustCompile(`^%[0-9]+$`)

type Snapshot struct {
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	Sessions  []Session `json:"sessions"`
}

type Session struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Attached bool     `json:"attached"`
	Windows  []Window `json:"windows"`
}

type Window struct {
	ID     string `json:"id"`
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Panes  []Pane `json:"panes"`
}

type Pane struct {
	ID             string `json:"id"`
	Index          int    `json:"index"`
	Active         bool   `json:"active"`
	PID            int    `json:"pid"`
	CurrentPath    string `json:"current_path"`
	CurrentCommand string `json:"current_command"`
	Dead           bool   `json:"dead"`
}

type Adapter struct {
	executor backend.Executor
	getenv   func(string) string
}

func New(executor backend.Executor, getenv func(string) string) *Adapter {
	return &Adapter{executor: executor, getenv: getenv}
}

func (adapter *Adapter) Name() backend.Name { return backend.Tmux }

// Snapshot reads tmux's live state. It deliberately does not persist a second
// session registry: tmux remains the lifecycle owner and every call refreshes
// from stable tmux identifiers.
func (adapter *Adapter) Snapshot(ctx context.Context) Snapshot {
	command, err := adapter.executor.LookPath("tmux")
	if err != nil {
		return Snapshot{Available: false, Reason: "tmux executable was not found", Sessions: []Session{}}
	}
	observeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	format := strings.Join([]string{
		"#{session_id}", "#{session_name}", "#{session_attached}",
		"#{window_id}", "#{window_index}", "#{window_name}", "#{window_active}",
		"#{pane_id}", "#{pane_index}", "#{pane_active}", "#{pane_pid}",
		"#{pane_current_path}", "#{pane_current_command}",
		"#{pane_dead}",
	}, snapshotSeparator)
	result, runErr := adapter.executor.Run(observeCtx, backend.ProcessRequest{Name: command, Args: []string{"list-panes", "-a", "-F", format}})
	if runErr != nil {
		reason := strings.TrimSpace(result.Stderr)
		if reason == "" {
			reason = "tmux server has no readable sessions"
		}
		return Snapshot{Available: false, Reason: reason, Sessions: []Session{}}
	}
	snapshot, parseErr := parseSnapshot(result.Stdout)
	if parseErr != nil {
		return Snapshot{Available: false, Reason: parseErr.Error(), Sessions: []Session{}}
	}
	return snapshot
}

func parseSnapshot(contents string) (Snapshot, error) {
	sessions := map[string]*Session{}
	type windowLocator struct {
		session *Session
		index   int
	}
	windows := map[string]windowLocator{}
	for lineNumber, line := range strings.Split(strings.TrimSpace(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, snapshotSeparator)
		if len(fields) != 14 {
			return Snapshot{}, fmt.Errorf("parse tmux snapshot line %d: expected 14 fields", lineNumber+1)
		}
		windowIndex, err := strconv.Atoi(fields[4])
		if err != nil {
			return Snapshot{}, fmt.Errorf("parse tmux window index on line %d: %w", lineNumber+1, err)
		}
		paneIndex, err := strconv.Atoi(fields[8])
		if err != nil {
			return Snapshot{}, fmt.Errorf("parse tmux pane index on line %d: %w", lineNumber+1, err)
		}
		pid, err := strconv.Atoi(fields[10])
		if err != nil {
			return Snapshot{}, fmt.Errorf("parse tmux pane pid on line %d: %w", lineNumber+1, err)
		}
		session := sessions[fields[0]]
		if session == nil {
			session = &Session{ID: fields[0], Name: fields[1], Attached: fields[2] != "0", Windows: []Window{}}
			sessions[fields[0]] = session
		}
		locator, found := windows[fields[3]]
		if !found {
			session.Windows = append(session.Windows, Window{ID: fields[3], Index: windowIndex, Name: fields[5], Active: fields[6] == "1", Panes: []Pane{}})
			locator = windowLocator{session: session, index: len(session.Windows) - 1}
			windows[fields[3]] = locator
		}
		window := &locator.session.Windows[locator.index]
		window.Panes = append(window.Panes, Pane{ID: fields[7], Index: paneIndex, Active: fields[9] == "1", PID: pid, CurrentPath: fields[11], CurrentCommand: fields[12], Dead: fields[13] == "1"})
	}
	result := Snapshot{Available: true, Sessions: make([]Session, 0, len(sessions))}
	for _, session := range sessions {
		sort.Slice(session.Windows, func(i, j int) bool { return session.Windows[i].Index < session.Windows[j].Index })
		for index := range session.Windows {
			sort.Slice(session.Windows[index].Panes, func(i, j int) bool {
				return session.Windows[index].Panes[i].Index < session.Windows[index].Panes[j].Index
			})
		}
		result.Sessions = append(result.Sessions, *session)
	}
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].Name < result.Sessions[j].Name })
	return result, nil
}

// Jump accepts only a stable tmux pane identifier. No shell command or free-form
// target is constructed from the request.
func (adapter *Adapter) Jump(ctx context.Context, paneID string, allowAttach bool) error {
	if !paneIDPattern.MatchString(paneID) {
		return fmt.Errorf("invalid tmux pane id %q", paneID)
	}
	command, err := adapter.executor.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux executable was not found: %w", err)
	}
	verified, err := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"display-message", "-p", "-t", paneID, "#{pane_id}" + snapshotSeparator + "#{session_name}"}})
	verification := strings.Split(strings.TrimSuffix(verified.Stdout, "\n"), snapshotSeparator)
	if err != nil || len(verification) != 2 || verification[0] != paneID || verification[1] == "" || strings.ContainsAny(verification[1], "\r\n") {
		return fmt.Errorf("tmux pane %s is unavailable", paneID)
	}
	sessionName := verification[1]
	if adapter.getenv != nil && adapter.getenv("TMUX") != "" {
		_, err = adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"switch-client", "-t", paneID}})
		if err != nil {
			return fmt.Errorf("switch tmux client to %s: %w", paneID, err)
		}
		return nil
	}
	if !allowAttach {
		return fmt.Errorf("tmux pane jump requires Workbench to run inside tmux")
	}
	_, err = adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"attach-session", "-t", "=" + sessionName, ";", "select-pane", "-t", paneID}, Interactive: true})
	if err != nil {
		return fmt.Errorf("attach tmux pane %s: %w", paneID, err)
	}
	return nil
}

func (adapter *Adapter) Detect(ctx context.Context, _ backend.OpenRequest) backend.Capability {
	command, err := adapter.executor.LookPath("tmux")
	if err != nil {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: "tmux executable was not found", Capabilities: []string{}}
	}
	detectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, versionErr := adapter.executor.Run(detectCtx, backend.ProcessRequest{Name: command, Args: []string{"-V"}})
	version := strings.TrimSpace(result.Stdout)
	if versionErr != nil {
		version = "unknown"
	}
	return backend.Capability{Backend: adapter.Name(), Available: true, Version: version, Capabilities: []string{"open_project", "list_sessions", "jump"}}
}

func (adapter *Adapter) OpenProject(ctx context.Context, request backend.OpenRequest) (backend.OpenResult, error) {
	path, err := projects.CanonicalPath(request.Project.Path)
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	command, err := adapter.executor.LookPath("tmux")
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	target := "=" + request.Project.ID
	if adapter.getenv != nil && adapter.getenv("TMUX") != "" {
		_, hasErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"has-session", "-t", target}})
		if hasErr != nil {
			created, createErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"new-session", "-d", "-s", request.Project.ID, "-c", path}})
			if createErr != nil {
				return adapter.result(request.Project.ID, created), fmt.Errorf("create tmux session: %w", createErr)
			}
		}
		switched, switchErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"switch-client", "-t", target}, Interactive: true})
		if switchErr != nil {
			return adapter.result(request.Project.ID, switched), fmt.Errorf("switch tmux client: %w", switchErr)
		}
		return adapter.result(request.Project.ID, switched), nil
	}
	opened, openErr := adapter.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"new-session", "-A", "-s", request.Project.ID, "-c", path}, Interactive: true})
	if openErr != nil {
		return adapter.result(request.Project.ID, opened), fmt.Errorf("open tmux session: %w", openErr)
	}
	return adapter.result(request.Project.ID, opened), nil
}

func (adapter *Adapter) result(projectID string, process backend.ProcessResult) backend.OpenResult {
	return backend.OpenResult{
		Backend: adapter.Name(), Reference: "tmux:" + projectID, Command: process.Command,
		ExitCode: process.ExitCode, Stdout: process.Stdout, Stderr: process.Stderr,
	}
}

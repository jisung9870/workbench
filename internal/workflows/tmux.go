package workflows

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
)

type TmuxLauncher struct{ executor backend.Executor }

func NewTmuxLauncher(executor backend.Executor) *TmuxLauncher {
	return &TmuxLauncher{executor: executor}
}

var panePattern = regexp.MustCompile(`^%[0-9]+$`)

func (l *TmuxLauncher) Launch(ctx context.Context, projectID, cwd, runID, executable string) (LaunchLocation, error) {
	tmux, err := l.executor.LookPath("tmux")
	if err != nil {
		return LaunchLocation{}, errorsUnavailable("tmux executable was not found")
	}
	target := "=" + projectID
	if _, err := l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"has-session", "-t", target}}); err != nil {
		if result, createErr := l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"new-session", "-d", "-s", projectID, "-c", cwd}}); createErr != nil {
			return LaunchLocation{}, fmt.Errorf("create tmux session: %s: %w", strings.TrimSpace(result.Stderr), createErr)
		}
	}
	window := "wf-" + runID[len(runID)-8:]
	workerCommand := "exec " + shellWord(executable) + " " + shellWord("workflows") + " " + shellWord("worker") + " " + shellWord(runID)
	result, err := l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", target, "-n", window, "-c", cwd, workerCommand}})
	if err != nil {
		return LaunchLocation{}, fmt.Errorf("create workflow window: %w", err)
	}
	pane := strings.TrimSpace(result.Stdout)
	if !panePattern.MatchString(pane) {
		return LaunchLocation{}, fmt.Errorf("tmux returned invalid pane %q", pane)
	}
	for key, value := range map[string]string{"@workbench_task_id": runID, "@workbench_workflow_run_id": runID} {
		if _, err := l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"set-option", "-p", "-t", pane, key, value}}); err != nil {
			return LaunchLocation{}, fmt.Errorf("set workflow pane ownership: %w", err)
		}
	}
	return LaunchLocation{PaneID: pane, SessionName: projectID}, nil
}

func (l *TmuxLauncher) VerifyWorker(ctx context.Context, runID string, getenv func(string) string) error {
	pane := ""
	if getenv != nil {
		pane = getenv("TMUX_PANE")
	}
	if !panePattern.MatchString(pane) {
		return &UnavailableError{Message: "workflow worker has no valid TMUX_PANE"}
	}
	tmux, err := l.executor.LookPath("tmux")
	if err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for {
		result, runErr := l.executor.Run(deadline, backend.ProcessRequest{Name: tmux, Args: []string{"display-message", "-p", "-t", pane, "#{@workbench_workflow_run_id}"}})
		if runErr == nil && strings.TrimSpace(result.Stdout) == runID {
			return nil
		}
		select {
		case <-deadline.Done():
			return &UnavailableError{Message: "workflow pane ownership handshake timed out"}
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *TmuxLauncher) Jump(ctx context.Context, run Result, allowAttach bool, getenv func(string) string) error {
	if !panePattern.MatchString(run.PaneID) {
		return &UnavailableError{Message: "workflow pane reference is invalid"}
	}
	tmux, err := l.executor.LookPath("tmux")
	if err != nil {
		return err
	}
	verified, err := l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"display-message", "-p", "-t", run.PaneID, "#{@workbench_task_id}"}})
	if err != nil || strings.TrimSpace(verified.Stdout) != run.ID {
		return &UnavailableError{Message: "workflow pane ownership could not be verified"}
	}
	if getenv != nil && getenv("TMUX") != "" {
		_, err = l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"switch-client", "-t", run.PaneID}})
		return err
	}
	if !allowAttach {
		return &UnavailableError{Message: "workflow jump requires Workbench to run inside tmux"}
	}
	_, err = l.executor.Run(ctx, backend.ProcessRequest{Name: tmux, Args: []string{"attach-session", "-t", "=" + run.SessionName, ";", "select-pane", "-t", run.PaneID}, Interactive: true})
	return err
}

func shellWord(value string) string          { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }
func errorsUnavailable(message string) error { return &UnavailableError{Message: message} }

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	wtadapter "github.com/jisung9870/workbench/adapters/windows_terminal"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/sessions"
)

type LaunchRequest struct {
	Task       Task
	Project    projects.Project
	Profile    config.Profile
	Executable string
	OnStarted  func(reference string, details map[string]string, pid int) error
}

type LaunchResult struct {
	Command  []string
	ExitCode int
	Waited   bool
}

type Runtime interface {
	Name() backend.Name
	Detect(context.Context, backend.OpenRequest) backend.Capability
	Launch(context.Context, LaunchRequest) (LaunchResult, error)
	Alive(context.Context, Task) (bool, error)
	Jump(context.Context, Task) error
	Stop(context.Context, Task) error
}

type UnsupportedError struct {
	Backend   backend.Name
	Operation string
	Reason    string
}

func (err *UnsupportedError) Error() string {
	return fmt.Sprintf("backend %q cannot %s: %s", err.Backend, err.Operation, err.Reason)
}

type UnsafeError struct {
	Message           string
	OwnershipMismatch bool
}

func (err *UnsafeError) Error() string { return err.Message }

type selectableRuntime struct{ Runtime }

func (runtime selectableRuntime) OpenProject(context.Context, backend.OpenRequest) (backend.OpenResult, error) {
	return backend.OpenResult{Backend: runtime.Name(), ExitCode: -1}, errors.New("agent runtime cannot open a project")
}

type ShellRuntime struct{ executor backend.Executor }

func NewShellRuntime(executor backend.Executor) *ShellRuntime {
	return &ShellRuntime{executor: executor}
}
func (runtime *ShellRuntime) Name() backend.Name { return backend.Shell }
func (runtime *ShellRuntime) Detect(_ context.Context, _ backend.OpenRequest) backend.Capability {
	return backend.Capability{Backend: runtime.Name(), Available: true, Version: "direct", Capabilities: []string{"agents_start"}}
}
func (runtime *ShellRuntime) Launch(ctx context.Context, request LaunchRequest) (LaunchResult, error) {
	process, err := runtime.executor.Run(ctx, backend.ProcessRequest{
		Dir: request.Task.CWD, Name: request.Executable, Interactive: true,
		Started: func(pid int) error {
			return request.OnStarted(fmt.Sprintf("process:%d", pid), map[string]string{"ownership": "attached"}, pid)
		},
	})
	return LaunchResult{Command: process.Command, ExitCode: process.ExitCode, Waited: true}, err
}
func (runtime *ShellRuntime) Alive(context.Context, Task) (bool, error) {
	return false, &UnsupportedError{Backend: runtime.Name(), Operation: "inspect", Reason: "attached process identity is not safely reconnectable"}
}
func (runtime *ShellRuntime) Jump(context.Context, Task) error {
	return &UnsupportedError{Backend: runtime.Name(), Operation: "jump", Reason: "the agent is attached to its original terminal"}
}
func (runtime *ShellRuntime) Stop(context.Context, Task) error {
	return &UnsupportedError{Backend: runtime.Name(), Operation: "stop", Reason: "PID reuse cannot be ruled out; stop it in the attached terminal"}
}

type TmuxRuntime struct {
	executor backend.Executor
	getenv   func(string) string
}

func NewTmuxRuntime(executor backend.Executor, getenv func(string) string) *TmuxRuntime {
	return &TmuxRuntime{executor: executor, getenv: getenv}
}
func (runtime *TmuxRuntime) Name() backend.Name { return backend.Tmux }
func (runtime *TmuxRuntime) Detect(ctx context.Context, _ backend.OpenRequest) backend.Capability {
	command, err := runtime.executor.LookPath("tmux")
	if err != nil {
		return backend.Capability{Backend: runtime.Name(), Available: false, Reason: "tmux executable was not found", Capabilities: []string{}}
	}
	detectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, versionErr := runtime.executor.Run(detectCtx, backend.ProcessRequest{Name: command, Args: []string{"-V"}})
	version := strings.TrimSpace(result.Stdout)
	if versionErr != nil {
		version = "unknown"
	}
	return backend.Capability{Backend: runtime.Name(), Available: true, Version: version, Capabilities: []string{"agents_start", "agents_jump", "agents_stop"}}
}
func (runtime *TmuxRuntime) Launch(ctx context.Context, request LaunchRequest) (LaunchResult, error) {
	session := request.Project.ID
	target := "=" + session
	if _, _, err := sessions.NewManager(runtime.executor, runtime.getenv).Ensure(ctx, request.Project); err != nil {
		return LaunchResult{ExitCode: -1}, fmt.Errorf("ensure tmux session: %w", err)
	}
	command, err := runtime.executor.LookPath("tmux")
	if err != nil {
		return LaunchResult{ExitCode: -1}, err
	}
	process, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{
		"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", target, "-n", request.Task.ID, "-c", request.Task.CWD, shellQuote(request.Executable),
	}})
	if err != nil {
		return launchFromProcess(process, false), fmt.Errorf("create tmux agent pane: %w", err)
	}
	pane := strings.TrimSpace(process.Stdout)
	if !regexp.MustCompile(`^%[0-9]+$`).MatchString(pane) {
		return launchFromProcess(process, false), fmt.Errorf("tmux returned an invalid pane reference %q", pane)
	}
	for key, value := range map[string]string{"@workbench_task_id": request.Task.ID, "@workbench_agent_kind": request.Task.AgentKind} {
		metadata, metadataErr := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"set-option", "-p", "-t", pane, key, value}})
		if metadataErr != nil {
			return launchFromProcess(metadata, false), fmt.Errorf("set tmux pane ownership metadata: %w", metadataErr)
		}
	}
	if err := request.OnStarted("tmux:"+pane, map[string]string{"session": session, "pane": pane}, 0); err != nil {
		return launchFromProcess(process, false), err
	}
	return launchFromProcess(process, false), nil
}
func (runtime *TmuxRuntime) Alive(ctx context.Context, task Task) (bool, error) {
	_, err := runtime.verify(ctx, task)
	if err == nil {
		return true, nil
	}
	var unsafe *UnsafeError
	if errors.As(err, &unsafe) {
		if unsafe.OwnershipMismatch {
			// A pane ID can refer to a different pane after the tmux server has
			// restarted. That must still block mutating operations, but for a
			// read-only liveness check it proves that the registered task no longer
			// owns a live pane and can be reconciled to a terminal state.
			return false, nil
		}
		return false, err
	}
	return false, nil
}
func (runtime *TmuxRuntime) Jump(ctx context.Context, task Task) error {
	command, err := runtime.verify(ctx, task)
	if err != nil {
		return err
	}
	pane := task.BackendDetails["pane"]
	if runtime.getenv != nil && runtime.getenv("TMUX") != "" {
		_, err = runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"switch-client", "-t", pane}, Interactive: true})
	} else {
		_, err = runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"attach-session", "-t", "=" + task.BackendDetails["session"], ";", "select-pane", "-t", pane}, Interactive: true})
	}
	return err
}
func (runtime *TmuxRuntime) Stop(ctx context.Context, task Task) error {
	command, err := runtime.verify(ctx, task)
	if err != nil {
		return err
	}
	_, err = runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"kill-pane", "-t", task.BackendDetails["pane"]}})
	return err
}
func (runtime *TmuxRuntime) verify(ctx context.Context, task Task) (string, error) {
	pane := task.BackendDetails["pane"]
	if task.BackendRef != "tmux:"+pane || !regexp.MustCompile(`^%[0-9]+$`).MatchString(pane) {
		return "", &UnsafeError{Message: fmt.Sprintf("task %q has an invalid tmux pane reference", task.ID)}
	}
	command, err := runtime.executor.LookPath("tmux")
	if err != nil {
		return "", err
	}
	result, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"display-message", "-p", "-t", pane, "#{@workbench_task_id}"}})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Stdout) != task.ID {
		return "", &UnsafeError{Message: fmt.Sprintf("tmux pane %s is not owned by task %q", pane, task.ID), OwnershipMismatch: true}
	}
	return command, nil
}

type CMUXRuntime struct {
	executor backend.Executor
	goos     string
}

func NewCMUXRuntime(executor backend.Executor, goos string) *CMUXRuntime {
	return &CMUXRuntime{executor: executor, goos: goos}
}
func (runtime *CMUXRuntime) Name() backend.Name { return backend.CMUX }
func (runtime *CMUXRuntime) Detect(ctx context.Context, _ backend.OpenRequest) backend.Capability {
	if runtime.goos != "darwin" {
		return backend.Capability{Backend: runtime.Name(), Available: false, Reason: "cmux is supported only on macOS", Capabilities: []string{}}
	}
	command, err := runtime.executor.LookPath("cmux")
	if err != nil {
		return backend.Capability{Backend: runtime.Name(), Available: false, Reason: "cmux executable was not found", Capabilities: []string{}}
	}
	result, pingErr := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"ping"}})
	if pingErr != nil {
		return backend.Capability{Backend: runtime.Name(), Available: false, Reason: strings.TrimSpace(result.Stderr), Capabilities: []string{}}
	}
	return backend.Capability{Backend: runtime.Name(), Available: true, Version: "socket-api", Capabilities: []string{"agents_start", "agents_jump", "agents_stop"}}
}
func (runtime *CMUXRuntime) Launch(ctx context.Context, request LaunchRequest) (LaunchResult, error) {
	command, err := runtime.executor.LookPath("cmux")
	if err != nil {
		return LaunchResult{ExitCode: -1}, err
	}
	created, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"new-workspace", "--json", "--id-format", "refs"}})
	if err != nil {
		return launchFromProcess(created, false), fmt.Errorf("create cmux workspace: %w", err)
	}
	workspace := findJSONReference(created.Stdout, "workspace")
	if workspace == "" {
		return launchFromProcess(created, false), fmt.Errorf("cmux did not return a workspace reference")
	}
	if err := request.OnStarted("cmux:"+workspace, map[string]string{"workspace": workspace}, 0); err != nil {
		return launchFromProcess(created, false), err
	}
	panels, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"list-panels", "--workspace", workspace, "--json", "--id-format", "refs"}})
	if err != nil {
		return launchFromProcess(panels, false), fmt.Errorf("list cmux workspace panels: %w", err)
	}
	surface := findJSONReference(panels.Stdout, "surface")
	if surface == "" {
		return launchFromProcess(panels, false), fmt.Errorf("cmux did not return a surface reference")
	}
	if err := request.OnStarted("cmux:"+workspace, map[string]string{"workspace": workspace, "surface": surface}, 0); err != nil {
		return launchFromProcess(panels, false), err
	}
	text := "cd -- " + shellQuote(request.Task.CWD) + " && exec " + shellQuote(request.Executable) + "\n"
	sent, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"send", "--surface", surface, text}})
	if err != nil {
		return launchFromProcess(sent, false), fmt.Errorf("start agent in cmux surface: %w", err)
	}
	return launchFromProcess(sent, false), nil
}
func (runtime *CMUXRuntime) Alive(ctx context.Context, task Task) (bool, error) {
	workspace, err := runtime.workspace(task)
	if err != nil {
		return false, err
	}
	command, err := runtime.executor.LookPath("cmux")
	if err != nil {
		return false, err
	}
	result, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"list-workspaces", "--json", "--id-format", "refs"}})
	if err != nil {
		return false, err
	}
	return containsJSONReference(result.Stdout, workspace), nil
}
func (runtime *CMUXRuntime) Jump(ctx context.Context, task Task) error {
	workspace, err := runtime.verifiedWorkspace(ctx, task)
	if err != nil {
		return err
	}
	command, _ := runtime.executor.LookPath("cmux")
	_, err = runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"select-workspace", "--workspace", workspace}})
	return err
}
func (runtime *CMUXRuntime) Stop(ctx context.Context, task Task) error {
	workspace, err := runtime.verifiedWorkspace(ctx, task)
	if err != nil {
		return err
	}
	command, _ := runtime.executor.LookPath("cmux")
	_, err = runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: []string{"close-workspace", "--workspace", workspace}})
	return err
}
func (runtime *CMUXRuntime) workspace(task Task) (string, error) {
	workspace := task.BackendDetails["workspace"]
	if workspace == "" || task.BackendRef != "cmux:"+workspace {
		return "", &UnsafeError{Message: fmt.Sprintf("task %q has an invalid cmux workspace reference", task.ID)}
	}
	return workspace, nil
}
func (runtime *CMUXRuntime) verifiedWorkspace(ctx context.Context, task Task) (string, error) {
	workspace, err := runtime.workspace(task)
	if err != nil {
		return "", err
	}
	alive, err := runtime.Alive(ctx, task)
	if err != nil {
		return "", err
	}
	if !alive {
		return "", &UnsafeError{Message: fmt.Sprintf("cmux workspace %q is no longer registered by cmux", workspace)}
	}
	return workspace, nil
}

type WindowsTerminalRuntime struct {
	executor backend.Executor
	goos     string
	getenv   func(string) string
	detect   *wtadapter.Adapter
}

func NewWindowsTerminalRuntime(executor backend.Executor, goos string, getenv func(string) string) *WindowsTerminalRuntime {
	return &WindowsTerminalRuntime{
		executor: executor, goos: goos, getenv: getenv,
		detect: wtadapter.New(executor, wtadapter.Environment{GOOS: goos, Getenv: getenv}),
	}
}
func (runtime *WindowsTerminalRuntime) Name() backend.Name { return backend.WindowsTerminal }
func (runtime *WindowsTerminalRuntime) Detect(ctx context.Context, request backend.OpenRequest) backend.Capability {
	capability := runtime.detect.Detect(ctx, request)
	if capability.Available {
		capability.Capabilities = []string{"agents_start"}
	}
	return capability
}
func (runtime *WindowsTerminalRuntime) Launch(ctx context.Context, request LaunchRequest) (LaunchResult, error) {
	command, err := runtime.executor.LookPath("wt.exe")
	if err != nil {
		return LaunchResult{ExitCode: -1}, err
	}
	args, err := wtadapter.LaunchPrefix(request.Profile)
	if err != nil {
		return LaunchResult{ExitCode: -1}, err
	}
	if profile := strings.TrimSpace(request.Profile.WindowsTerminalProfile); profile != "" {
		args = append(args, "--profile", profile)
	}
	if runtime.goos == "windows" && request.Project.WindowsWSL == nil {
		args = append(args, "--startingDirectory", request.Task.CWD, request.Executable)
	} else {
		if runtime.goos == "windows" && request.Task.WorktreeID != "" {
			return LaunchResult{ExitCode: -1}, fmt.Errorf("native Windows worktree %q has no explicit WSL path mapping", request.Task.WorktreeID)
		}
		args = append(args, "wsl.exe")
		distro := strings.TrimSpace(request.Profile.WindowsTerminalDistro)
		if distro == "" {
			distro = runtime.get("WSL_DISTRO_NAME")
		}
		wslPath := request.Task.CWD
		if request.Project.WindowsWSL != nil {
			distro = request.Project.WindowsWSL.Distro
			wslPath = request.Project.WindowsWSL.WSLPath
		}
		if distro != "" {
			args = append(args, "-d", distro)
		}
		args = append(args, "--cd", wslPath, request.Task.AgentKind)
	}
	process, err := runtime.executor.Run(ctx, backend.ProcessRequest{Name: command, Args: args})
	if err == nil {
		err = request.OnStarted("windows-terminal:"+request.Task.ID, map[string]string{"ownership": "launch-only"}, 0)
	}
	return launchFromProcess(process, false), err
}
func (runtime *WindowsTerminalRuntime) Alive(context.Context, Task) (bool, error) {
	return false, &UnsupportedError{Backend: runtime.Name(), Operation: "inspect", Reason: "wt.exe does not expose stable tab ownership for this launch"}
}
func (runtime *WindowsTerminalRuntime) Jump(context.Context, Task) error {
	return &UnsupportedError{Backend: runtime.Name(), Operation: "jump", Reason: "the launched tab has no stable address"}
}
func (runtime *WindowsTerminalRuntime) Stop(context.Context, Task) error {
	return &UnsupportedError{Backend: runtime.Name(), Operation: "stop", Reason: "the launched tab has no safely verifiable ownership reference"}
}
func (runtime *WindowsTerminalRuntime) get(key string) string {
	if runtime.getenv == nil {
		return ""
	}
	return runtime.getenv(key)
}

func launchFromProcess(process backend.ProcessResult, waited bool) LaunchResult {
	return LaunchResult{Command: process.Command, ExitCode: process.ExitCode, Waited: waited}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func findJSONReference(contents, kind string) string {
	var value any
	if json.Unmarshal([]byte(contents), &value) == nil {
		if found := findReferenceValue(value, kind); found != "" {
			return found
		}
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(kind) + `:[A-Za-z0-9._-]+`)
	return pattern.FindString(contents)
}

func findReferenceValue(value any, kind string) string {
	switch current := value.(type) {
	case map[string]any:
		for _, key := range []string{kind + "_id", kind, "ref", "id"} {
			if raw, exists := current[key]; exists {
				if text, ok := raw.(string); ok && (strings.HasPrefix(text, kind+":") || key == kind+"_id" || key == kind) {
					return text
				}
			}
		}
		for _, nested := range current {
			if found := findReferenceValue(nested, kind); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range current {
			if found := findReferenceValue(nested, kind); found != "" {
				return found
			}
		}
	case string:
		if strings.HasPrefix(current, kind+":") {
			return current
		}
	}
	return ""
}

func containsJSONReference(contents, reference string) bool {
	var value any
	if json.Unmarshal([]byte(contents), &value) != nil {
		return false
	}
	return walkStrings(value, func(candidate string) bool { return candidate == reference })
}

func walkStrings(value any, match func(string) bool) bool {
	switch current := value.(type) {
	case map[string]any:
		for _, nested := range current {
			if walkStrings(nested, match) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if walkStrings(nested, match) {
				return true
			}
		}
	case string:
		return match(current)
	}
	return false
}

package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/worktrees"
)

type Scope string

const (
	Core     Scope = "core"
	Optional Scope = "optional"
	Disabled Scope = "disabled"
)

type Status string

const (
	Available   Status = "available"
	Unavailable Status = "unavailable"
	Skipped     Status = "skipped"
)

type Capability struct {
	Name         string   `json:"name"`
	Scope        Scope    `json:"scope"`
	Status       Status   `json:"status"`
	Available    bool     `json:"available"`
	Description  string   `json:"description"`
	Path         string   `json:"path,omitempty"`
	Version      string   `json:"version,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Recovery     string   `json:"recovery,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type Summary struct {
	Available         int `json:"available"`
	UnavailableCore   int `json:"unavailable_core"`
	UnavailableOption int `json:"unavailable_optional"`
	Skipped           int `json:"skipped"`
}

type Report struct {
	Platform     string       `json:"platform"`
	Profile      string       `json:"profile"`
	Capabilities []Capability `json:"capabilities"`
	Summary      Summary      `json:"summary"`
}

func (report Report) Healthy(strict bool) bool {
	if report.Summary.UnavailableCore > 0 {
		return false
	}
	return !strict || report.Summary.UnavailableOption == 0
}

func (report Report) Warnings() []string {
	warnings := []string{}
	for _, capability := range report.Capabilities {
		if capability.Scope == Optional && capability.Status == Unavailable {
			warnings = append(warnings, fmt.Sprintf("optional capability unavailable: %s", capability.Name))
		}
	}
	return warnings
}

func (report Report) Missing(scope Scope) []string {
	missing := []string{}
	for _, capability := range report.Capabilities {
		if capability.Scope == scope && capability.Status == Unavailable {
			missing = append(missing, capability.Name)
		}
	}
	return missing
}

type Manager struct {
	paths    config.Paths
	executor backend.Executor
	env      backend.Environment
	backends *backend.Registry
}

func NewManager(paths config.Paths, executor backend.Executor, environment backend.Environment, registry *backend.Registry) *Manager {
	return &Manager{paths: paths, executor: executor, env: environment, backends: registry}
}

func (manager *Manager) Run(ctx context.Context, requestedProfile string) Report {
	profileName := requestedProfile
	settings, settingsErr := config.LoadSettings(manager.paths.ConfigFile)
	if profileName == "" {
		if settingsErr == nil {
			profileName = settings.ActiveProfile
		} else {
			profileName = config.DefaultSettings().ActiveProfile
		}
	}
	profile, profileErr := config.LoadProfile(manager.paths, profileName)
	if profileErr != nil {
		profile = config.DefaultProfile()
	}

	report := Report{Platform: manager.platform(), Profile: profileName, Capabilities: []Capability{}}
	add := func(capability Capability) { report.Capabilities = append(report.Capabilities, capability) }

	configErr := config.Validate(manager.paths)
	add(checkResult("config", Core, "Workbench settings and profiles", configErr, "fix the reported TOML field or run wb config validate"))
	if settingsErr != nil && configErr == nil {
		add(checkResult("settings", Core, "active Workbench settings", settingsErr, "fix config.toml or remove it to use defaults"))
	}
	add(checkResult("profile:"+profileName, Core, "selected Workbench profile", profileErr, "create or fix the selected profile TOML"))

	projectStore := projects.NewStore(manager.paths)
	projectRegistry, projectErr := projectStore.Load()
	add(checkResult("projects-state", Core, "schema-v1 project registry", projectErr, "fix projects.toml or restore a backup"))
	if projectErr == nil {
		for _, project := range projectRegistry.Projects {
			canonical, err := projects.CanonicalPath(project.Path)
			if err == nil && filepath.Clean(canonical) != filepath.Clean(project.Path) {
				err = fmt.Errorf("canonical path changed to %s", canonical)
			}
			add(checkResult("project:"+project.ID, Optional, "registered project path", err, "update the project path for this machine or remove the stale registry entry"))
		}
	}

	_, agentErr := agents.NewStateStore(manager.paths).Load()
	add(checkResult("agents-state", Core, "schema-v1 Agent task registry", agentErr, "fix agents.json or restore a backup"))
	_, worktreeErr := worktrees.NewStateStore(manager.paths).Load()
	add(checkResult("worktrees-state", Core, "schema-v1 managed worktree registry", worktreeErr, "fix worktrees.json or restore a backup"))

	add(manager.command(ctx, toolSpec{Name: "git", Scope: Core, Description: "worktree and repository provider", VersionArgs: []string{"--version"}, Recovery: manager.installHint("brew install git", "sudo apt install git", "install Git for Windows")}))
	for _, tool := range []toolSpec{
		{Name: "bb", Scope: Optional, Description: "binbox compatibility provider", Recovery: "install or link binbox so bb is on PATH"},
		{Name: "nvim", Scope: Optional, Description: "LazyVim/editor client", Recovery: manager.installHint("brew install neovim", "sudo apt install neovim", "install Neovim")},
		{Name: "codex", Scope: Optional, Description: "Codex coding Agent", Recovery: "install Codex CLI and authenticate it"},
		{Name: "claude", Scope: Optional, Description: "Claude Code coding Agent", Recovery: "install Claude Code and authenticate it"},
	} {
		add(manager.command(ctx, tool))
	}

	request := backend.OpenRequest{Project: projects.Project{ID: "doctor", DefaultBackend: "auto"}, Profile: profile}
	for _, detected := range manager.backends.Capabilities(ctx, request) {
		scope := Optional
		status := Unavailable
		if detected.Backend == backend.Shell {
			scope = Core
		}
		if detected.Backend == backend.CMUX && manager.env.GOOS != "darwin" {
			scope, status = Disabled, Skipped
		}
		if detected.Backend == backend.WindowsTerminal && manager.env.GOOS != "windows" && !manager.env.IsWSL() {
			scope, status = Disabled, Skipped
		}
		if detected.Available {
			status = Available
		}
		capability := Capability{
			Name: "backend:" + string(detected.Backend), Scope: scope, Status: status,
			Available: detected.Available, Description: "terminal backend", Version: detected.Version,
			Reason: detected.Reason, Capabilities: nonNil(detected.Capabilities),
		}
		if status == Unavailable {
			capability.Recovery = backendRecovery(detected.Backend)
		}
		add(capability)
	}

	report.Summary = summarize(report.Capabilities)
	return report
}

type toolSpec struct {
	Name        string
	Scope       Scope
	Description string
	VersionArgs []string
	Recovery    string
}

func (manager *Manager) command(ctx context.Context, tool toolSpec) Capability {
	capability := Capability{Name: tool.Name, Scope: tool.Scope, Status: Unavailable, Description: tool.Description, Recovery: tool.Recovery}
	path, err := manager.executor.LookPath(tool.Name)
	if err != nil {
		capability.Reason = fmt.Sprintf("%s executable was not found", tool.Name)
		return capability
	}
	capability.Available = true
	capability.Status = Available
	capability.Path = path
	capability.Recovery = ""
	if len(tool.VersionArgs) == 0 {
		return capability
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, probeErr := manager.executor.Run(probeCtx, backend.ProcessRequest{Name: path, Args: tool.VersionArgs})
	version := firstLine(result.Stdout)
	if version == "" {
		version = firstLine(result.Stderr)
	}
	if probeErr == nil {
		capability.Version = version
	}
	return capability
}

func checkResult(name string, scope Scope, description string, err error, recovery string) Capability {
	capability := Capability{Name: name, Scope: scope, Status: Available, Available: true, Description: description}
	if err != nil {
		capability.Status = Unavailable
		capability.Available = false
		capability.Reason = err.Error()
		capability.Recovery = recovery
	}
	return capability
}

func summarize(capabilities []Capability) Summary {
	summary := Summary{}
	for _, capability := range capabilities {
		switch capability.Status {
		case Available:
			summary.Available++
		case Skipped:
			summary.Skipped++
		case Unavailable:
			if capability.Scope == Core {
				summary.UnavailableCore++
			} else {
				summary.UnavailableOption++
			}
		}
	}
	return summary
}

func (manager *Manager) platform() string {
	if manager.env.IsWSL() {
		return "windows-wsl"
	}
	return manager.env.GOOS
}

func (manager *Manager) installHint(darwin, linux, windows string) string {
	switch {
	case manager.env.GOOS == "darwin":
		return darwin
	case manager.env.GOOS == "windows":
		return windows
	default:
		return linux
	}
}

func backendRecovery(name backend.Name) string {
	switch name {
	case backend.CMUX:
		return "install cmux on macOS and enable its CLI/socket access"
	case backend.WindowsTerminal:
		return "install Windows Terminal or enable WSL interop for wt.exe"
	case backend.Tmux:
		return "install tmux or select the shell backend"
	case backend.Shell:
		return "set SHELL to an installed interactive shell"
	default:
		return "install or configure the backend"
	}
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return value
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

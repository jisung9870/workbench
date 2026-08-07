package backend

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type Name string

const (
	Auto            Name = "auto"
	CMUX            Name = "cmux"
	WindowsTerminal Name = "windows-terminal"
	Tmux            Name = "tmux"
	Shell           Name = "shell"
)

type Capability struct {
	Backend      Name     `json:"backend"`
	Available    bool     `json:"available"`
	Version      string   `json:"version,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Capabilities []string `json:"capabilities"`
}

type OpenRequest struct {
	Project projects.Project
	Profile config.Profile
	Session Name
}

type OpenResult struct {
	Backend   Name     `json:"backend"`
	Session   Name     `json:"session,omitempty"`
	Surface   Name     `json:"surface,omitempty"`
	Reference string   `json:"reference"`
	Command   []string `json:"command"`
	ExitCode  int      `json:"exit_code"`
	Stdout    string   `json:"-"`
	Stderr    string   `json:"-"`
}

type Adapter interface {
	Name() Name
	Detect(context.Context, OpenRequest) Capability
	OpenProject(context.Context, OpenRequest) (OpenResult, error)
}

type Environment struct {
	GOOS   string
	Getenv func(string) string
}

func CurrentEnvironment() Environment {
	return Environment{GOOS: runtime.GOOS, Getenv: os.Getenv}
}

func (environment Environment) get(key string) string {
	if environment.Getenv == nil {
		return ""
	}
	return environment.Getenv(key)
}

func (environment Environment) IsSSH() bool {
	return environment.get("SSH_CONNECTION") != "" || environment.get("SSH_CLIENT") != "" || environment.get("SSH_TTY") != ""
}

func (environment Environment) IsWSL() bool {
	return environment.get("WSL_INTEROP") != "" || environment.get("WSL_DISTRO_NAME") != ""
}

type Registry struct {
	adapters map[Name]Adapter
	env      Environment
}

func NewRegistry(environment Environment, adapters ...Adapter) *Registry {
	registered := make(map[Name]Adapter, len(adapters))
	for _, adapter := range adapters {
		registered[adapter.Name()] = adapter
	}
	return &Registry{adapters: registered, env: environment}
}

type Selection struct {
	Adapter  Adapter
	Warnings []string
	Session  Name
	Surface  Name
}

type UnavailableError struct {
	Backend  Name
	Reason   string
	Fallback []Name
}

func (err *UnavailableError) Error() string {
	return fmt.Sprintf("backend %q is unavailable: %s", err.Backend, err.Reason)
}

func (registry *Registry) Select(ctx context.Context, request OpenRequest, requested Name) (Selection, error) {
	if requested == "" {
		requested = Auto
	}
	if requested != Auto {
		adapter, exists := registry.adapters[requested]
		if !exists {
			return Selection{}, fmt.Errorf("unknown backend %q", requested)
		}
		capability := adapter.Detect(ctx, request)
		if !capability.Available {
			return Selection{}, &UnavailableError{Backend: requested, Reason: capability.Reason, Fallback: registry.availableFallbacks(ctx, request, requested)}
		}
		return Selection{Adapter: adapter, Warnings: []string{}}, nil
	}

	warnings := []string{}
	tried := map[Name]struct{}{}
	try := func(name Name, source string, warn bool) Adapter {
		if name == "" || name == Auto {
			return nil
		}
		if _, exists := tried[name]; exists {
			return nil
		}
		tried[name] = struct{}{}
		adapter, exists := registry.adapters[name]
		if !exists {
			if warn {
				warnings = append(warnings, fmt.Sprintf("%s backend %q is not registered", source, name))
			}
			return nil
		}
		capability := adapter.Detect(ctx, request)
		if !capability.Available {
			if warn {
				warnings = append(warnings, fmt.Sprintf("%s backend %q unavailable: %s", source, name, capability.Reason))
			}
			return nil
		}
		return adapter
	}

	if adapter := try(Name(request.Project.DefaultBackend), "project override", true); adapter != nil {
		return Selection{Adapter: adapter, Warnings: warnings}, nil
	}
	if adapter := try(Name(request.Profile.DefaultBackend), "profile default", true); adapter != nil {
		return Selection{Adapter: adapter, Warnings: warnings}, nil
	}
	if request.Profile.PreferCurrentTmux && registry.env.get("TMUX") != "" {
		if adapter := try(Tmux, "current tmux client", true); adapter != nil {
			return Selection{Adapter: adapter, Warnings: warnings}, nil
		}
	}
	if len(request.Profile.BackendPriority) > 0 {
		for _, configured := range request.Profile.BackendPriority {
			name := Name(configured)
			if name == CMUX && registry.env.IsSSH() {
				continue
			}
			if adapter := try(name, "profile backend_priority", false); adapter != nil {
				return Selection{Adapter: adapter, Warnings: warnings}, nil
			}
		}
	}
	if registry.env.GOOS == "windows" {
		if adapter := try(WindowsTerminal, "Windows native auto-detection", false); adapter != nil {
			return Selection{Adapter: adapter, Warnings: warnings}, nil
		}
	}
	if !registry.env.IsSSH() {
		if adapter := try(CMUX, "cmux auto-detection", false); adapter != nil {
			return Selection{Adapter: adapter, Warnings: warnings}, nil
		}
	}
	if registry.env.get("TMUX") != "" || registry.env.IsSSH() || registry.env.IsWSL() {
		if adapter := try(Tmux, "terminal environment auto-detection", false); adapter != nil {
			return Selection{Adapter: adapter, Warnings: warnings}, nil
		}
	}
	if adapter := try(Shell, "shell fallback", false); adapter != nil {
		return Selection{Adapter: adapter, Warnings: warnings}, nil
	}
	return Selection{}, &UnavailableError{Backend: Auto, Reason: "no usable backend was detected", Fallback: []Name{Shell}}
}

func (registry *Registry) availableFallbacks(ctx context.Context, request OpenRequest, excluded Name) []Name {
	order := []Name{Tmux, Shell, WindowsTerminal, CMUX}
	available := []Name{}
	for _, name := range order {
		if name == excluded {
			continue
		}
		adapter, exists := registry.adapters[name]
		if exists && adapter.Detect(ctx, request).Available {
			available = append(available, name)
		}
	}
	return available
}

func (registry *Registry) Capabilities(ctx context.Context, request OpenRequest) []Capability {
	capabilities := make([]Capability, 0, len(registry.adapters))
	for _, adapter := range registry.adapters {
		capabilities = append(capabilities, adapter.Detect(ctx, request))
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Backend < capabilities[j].Backend })
	return capabilities
}

func ParseName(value string) (Name, error) {
	value = strings.TrimSpace(value)
	if !config.ValidBackend(value) {
		return "", fmt.Errorf("invalid backend %q", value)
	}
	return Name(value), nil
}

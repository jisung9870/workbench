package windows_terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type Environment struct {
	GOOS   string
	Getenv func(string) string
}

type Adapter struct {
	executor backend.Executor
	env      Environment
}

func New(executor backend.Executor, environment Environment) *Adapter {
	return &Adapter{executor: executor, env: environment}
}

func (adapter *Adapter) Name() backend.Name { return backend.WindowsTerminal }

func (adapter *Adapter) Detect(_ context.Context, request backend.OpenRequest) backend.Capability {
	if adapter.env.GOOS != "windows" && !adapter.isWSL() {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: "Windows Terminal requires native Windows or WSL interop", Capabilities: []string{}}
	}
	if _, err := adapter.executor.LookPath("wt.exe"); err != nil {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: "wt.exe was not found; install Windows Terminal or choose tmux/shell", Capabilities: []string{}}
	}
	if _, _, err := launchPreferences(request.Profile); err != nil {
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: err.Error(), Capabilities: []string{}}
	}
	requestedProfile := strings.TrimSpace(request.Profile.WindowsTerminalProfile)
	profiles, known := adapter.profiles()
	if requestedProfile != "" && known && !profiles.contains(requestedProfile) {
		reason := fmt.Sprintf("Windows Terminal profile %q was not found", requestedProfile)
		if len(profiles.names) > 0 {
			reason += fmt.Sprintf("; available profiles: %s", strings.Join(profiles.names, ", "))
		}
		reason += "; update windows_terminal_profile or choose tmux/shell"
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: reason, Capabilities: []string{}}
	}
	if adapter.env.GOOS != "windows" {
		if distro := adapter.distro(request); distro == "" {
			return backend.Capability{
				Backend: adapter.Name(), Available: false,
				Reason:       "WSL distribution is unknown; set windows_terminal_distro or WSL_DISTRO_NAME",
				Capabilities: []string{},
			}
		}
	}
	return backend.Capability{Backend: adapter.Name(), Available: true, Version: "detected", Capabilities: []string{"open_project", "new_window", "new_tab", "split_pane", "set_starting_directory", "open_wsl_profile"}}
}

func (adapter *Adapter) OpenProject(ctx context.Context, request backend.OpenRequest) (backend.OpenResult, error) {
	path, err := projects.CanonicalPath(request.Project.Path)
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	capability := adapter.Detect(ctx, request)
	if !capability.Available {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), fmt.Errorf("%s", capability.Reason)
	}
	command, err := adapter.executor.LookPath("wt.exe")
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	args, err := LaunchPrefix(request.Profile)
	if err != nil {
		return adapter.result(request.Project.ID, backend.ProcessResult{}), err
	}
	if profile := strings.TrimSpace(request.Profile.WindowsTerminalProfile); profile != "" {
		args = append(args, "--profile", profile)
	}
	if adapter.env.GOOS == "windows" && request.Project.WindowsWSL == nil {
		args = append(args, "--startingDirectory", path)
	} else {
		distro := adapter.distro(request)
		wslPath := path
		if request.Project.WindowsWSL != nil {
			wslPath = request.Project.WindowsWSL.WSLPath
		}
		args = append(args, "wsl.exe")
		if distro != "" {
			args = append(args, "-d", distro)
		}
		args = append(args, "--cd", wslPath)
	}
	launchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	process, runErr := adapter.executor.Run(launchCtx, backend.ProcessRequest{Name: command, Args: args})
	if runErr != nil {
		return adapter.result(request.Project.ID, process), fmt.Errorf("launch Windows Terminal: %w", runErr)
	}
	return adapter.result(request.Project.ID, process), nil
}

type profileCatalog struct {
	names       []string
	identifiers map[string]struct{}
}

func (catalog profileCatalog) contains(value string) bool {
	_, exists := catalog.identifiers[strings.ToLower(strings.TrimSpace(value))]
	return exists
}

func (adapter *Adapter) profiles() (profileCatalog, bool) {
	candidates := []string{}
	if explicit := strings.TrimSpace(adapter.getenv("WT_SETTINGS_PATH")); explicit != "" {
		candidates = append(candidates, explicit)
	}
	if adapter.env.GOOS == "windows" {
		if localAppData := strings.TrimSpace(adapter.getenv("LOCALAPPDATA")); localAppData != "" {
			candidates = append(candidates,
				filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbwe", "LocalState", "settings.json"),
				filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe", "LocalState", "settings.json"),
				filepath.Join(localAppData, "Microsoft", "Windows Terminal", "settings.json"),
			)
		}
	}
	for _, path := range candidates {
		contents, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var settings struct {
			Profiles struct {
				List []struct {
					Name   string `json:"name"`
					GUID   string `json:"guid"`
					Hidden bool   `json:"hidden"`
				} `json:"list"`
			} `json:"profiles"`
		}
		if err := json.Unmarshal(normalizeJSONC(contents), &settings); err != nil {
			return profileCatalog{}, false
		}
		names := []string{}
		identifiers := map[string]struct{}{}
		for _, profile := range settings.Profiles.List {
			if profile.Hidden {
				continue
			}
			if profile.Name != "" {
				names = append(names, profile.Name)
				identifiers[strings.ToLower(profile.Name)] = struct{}{}
			}
			if profile.GUID != "" {
				identifiers[strings.ToLower(profile.GUID)] = struct{}{}
			}
		}
		sort.Strings(names)
		return profileCatalog{names: names, identifiers: identifiers}, true
	}
	return profileCatalog{}, false
}

func normalizeJSONC(contents []byte) []byte {
	withoutComments := make([]byte, 0, len(contents))
	inString := false
	escaped := false
	lineComment := false
	blockComment := false
	for index := 0; index < len(contents); index++ {
		character := contents[index]
		if lineComment {
			if character == '\n' {
				lineComment = false
				withoutComments = append(withoutComments, character)
			} else {
				withoutComments = append(withoutComments, ' ')
			}
			continue
		}
		if blockComment {
			if character == '*' && index+1 < len(contents) && contents[index+1] == '/' {
				withoutComments = append(withoutComments, ' ', ' ')
				index++
				blockComment = false
			} else if character == '\n' {
				withoutComments = append(withoutComments, '\n')
			} else {
				withoutComments = append(withoutComments, ' ')
			}
			continue
		}
		if inString {
			withoutComments = append(withoutComments, character)
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			withoutComments = append(withoutComments, character)
			continue
		}
		if character == '/' && index+1 < len(contents) && contents[index+1] == '/' {
			withoutComments = append(withoutComments, ' ', ' ')
			index++
			lineComment = true
			continue
		}
		if character == '/' && index+1 < len(contents) && contents[index+1] == '*' {
			withoutComments = append(withoutComments, ' ', ' ')
			index++
			blockComment = true
			continue
		}
		withoutComments = append(withoutComments, character)
	}

	normalized := make([]byte, 0, len(withoutComments))
	inString = false
	escaped = false
	for index := 0; index < len(withoutComments); index++ {
		character := withoutComments[index]
		if inString {
			normalized = append(normalized, character)
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			normalized = append(normalized, character)
			continue
		}
		if character == ',' {
			next := index + 1
			for next < len(withoutComments) && (withoutComments[next] == ' ' || withoutComments[next] == '\t' || withoutComments[next] == '\r' || withoutComments[next] == '\n') {
				next++
			}
			if next < len(withoutComments) && (withoutComments[next] == '}' || withoutComments[next] == ']') {
				continue
			}
		}
		normalized = append(normalized, character)
	}
	return normalized
}

func (adapter *Adapter) result(projectID string, process backend.ProcessResult) backend.OpenResult {
	return backend.OpenResult{
		Backend: adapter.Name(), Reference: "windows-terminal:" + projectID, Command: process.Command,
		ExitCode: process.ExitCode, Stdout: process.Stdout, Stderr: process.Stderr,
	}
}

func (adapter *Adapter) isWSL() bool {
	return adapter.getenv("WSL_INTEROP") != "" || adapter.getenv("WSL_DISTRO_NAME") != ""
}

func (adapter *Adapter) distro(request backend.OpenRequest) string {
	if request.Project.WindowsWSL != nil {
		return strings.TrimSpace(request.Project.WindowsWSL.Distro)
	}
	if distro := strings.TrimSpace(request.Profile.WindowsTerminalDistro); distro != "" {
		return distro
	}
	return strings.TrimSpace(adapter.getenv("WSL_DISTRO_NAME"))
}

func (adapter *Adapter) getenv(key string) string {
	if adapter.env.Getenv == nil {
		return ""
	}
	return adapter.env.Getenv(key)
}

func launchPreferences(profile config.Profile) (string, string, error) {
	window := strings.TrimSpace(profile.WindowsTerminalWindow)
	if window == "" {
		window = "last"
	}
	mode := strings.TrimSpace(profile.WindowsTerminalMode)
	if mode == "" {
		mode = "tab"
	}
	if !config.ValidWindowsTerminalWindow(window) {
		return "", "", fmt.Errorf("invalid Windows Terminal window %q", window)
	}
	if !config.ValidWindowsTerminalMode(mode) {
		return "", "", fmt.Errorf("invalid Windows Terminal mode %q", mode)
	}
	if !config.ValidWindowsTerminalDistro(profile.WindowsTerminalDistro) {
		return "", "", fmt.Errorf("invalid Windows Terminal distribution %q", profile.WindowsTerminalDistro)
	}
	return window, mode, nil
}

func LaunchPrefix(profile config.Profile) ([]string, error) {
	window, mode, err := launchPreferences(profile)
	if err != nil {
		return nil, err
	}
	args := []string{"--window", window}
	switch mode {
	case "tab":
		args = append(args, "new-tab")
	case "split-auto":
		args = append(args, "split-pane")
	case "split-horizontal":
		args = append(args, "split-pane", "--horizontal")
	case "split-vertical":
		args = append(args, "split-pane", "--vertical")
	}
	return args, nil
}

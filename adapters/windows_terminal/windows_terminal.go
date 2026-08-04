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
	requestedProfile := strings.TrimSpace(request.Profile.WindowsTerminalProfile)
	profiles, known := adapter.profileNames()
	if requestedProfile != "" && known && !contains(profiles, requestedProfile) {
		reason := fmt.Sprintf("Windows Terminal profile %q was not found", requestedProfile)
		if len(profiles) > 0 {
			reason += fmt.Sprintf("; available profiles: %s", strings.Join(profiles, ", "))
		}
		reason += "; update windows_terminal_profile or choose tmux/shell"
		return backend.Capability{Backend: adapter.Name(), Available: false, Reason: reason, Capabilities: []string{}}
	}
	return backend.Capability{Backend: adapter.Name(), Available: true, Version: "detected", Capabilities: []string{"open_project", "set_starting_directory", "open_wsl_profile"}}
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
	args := []string{"-w", "0", "new-tab"}
	if profile := strings.TrimSpace(request.Profile.WindowsTerminalProfile); profile != "" {
		args = append(args, "--profile", profile)
	}
	if adapter.env.GOOS == "windows" && request.Project.WindowsWSL == nil {
		args = append(args, "--startingDirectory", path)
	} else {
		distro := adapter.getenv("WSL_DISTRO_NAME")
		wslPath := path
		if request.Project.WindowsWSL != nil {
			distro = request.Project.WindowsWSL.Distro
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

func (adapter *Adapter) profileNames() ([]string, bool) {
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
					Hidden bool   `json:"hidden"`
				} `json:"list"`
			} `json:"profiles"`
		}
		if err := json.Unmarshal(normalizeJSONC(contents), &settings); err != nil {
			return []string{}, false
		}
		names := []string{}
		for _, profile := range settings.Profiles.List {
			if !profile.Hidden && profile.Name != "" {
				names = append(names, profile.Name)
			}
		}
		sort.Strings(names)
		return names, true
	}
	return []string{}, false
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

func (adapter *Adapter) getenv(key string) string {
	if adapter.env.Getenv == nil {
		return ""
	}
	return adapter.env.Getenv(key)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

package windows_terminal

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeExecutor struct {
	request backend.ProcessRequest
}

func (executor *fakeExecutor) LookPath(name string) (string, error) { return name, nil }

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.request = request
	return backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}, nil
}

func TestDetectReportsMissingConfiguredProfile(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	contents := `{
  // Windows Terminal settings are JSONC.
  "profiles": {"list": [
    {"name": "Ubuntu-24.04"},
    {"name": "PowerShell"},
  ]},
}`
	if err := os.WriteFile(settings, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := New(&fakeExecutor{}, Environment{GOOS: "linux", Getenv: func(key string) string {
		switch key {
		case "WSL_INTEROP":
			return "/run/WSL/1_interop"
		case "WT_SETTINGS_PATH":
			return settings
		default:
			return ""
		}
	}})
	capability := adapter.Detect(context.Background(), backend.OpenRequest{Profile: config.Profile{WindowsTerminalProfile: "Missing"}})
	if capability.Available || !strings.Contains(capability.Reason, "available profiles: PowerShell, Ubuntu-24.04") {
		t.Fatalf("unexpected capability: %#v", capability)
	}
}

func TestWSLOpenUsesExplicitWtAndWslArgumentArray(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, Environment{GOOS: "linux", Getenv: func(key string) string {
		switch key {
		case "WSL_INTEROP":
			return "/run/WSL/1_interop"
		case "WSL_DISTRO_NAME":
			return "Ubuntu-24.04"
		default:
			return ""
		}
	}})
	path := t.TempDir()
	result, err := adapter.OpenProject(context.Background(), backend.OpenRequest{
		Project: projects.Project{ID: "alpha", Path: path},
		Profile: config.Profile{WindowsTerminalProfile: "Ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-w", "0", "new-tab", "--profile", "Ubuntu-24.04", "wsl.exe", "-d", "Ubuntu-24.04", "--cd", path}
	if executor.request.Name != "wt.exe" || !reflect.DeepEqual(executor.request.Args, want) {
		t.Fatalf("unexpected Windows Terminal command: %#v", executor.request)
	}
	if result.Reference != "windows-terminal:alpha" {
		t.Fatalf("unexpected reference: %s", result.Reference)
	}
}

func TestWindowsWSLOverlayIsUsedWithoutPathInference(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, Environment{GOOS: "windows", Getenv: func(string) string { return "" }})
	nativePath := t.TempDir()
	_, err := adapter.OpenProject(context.Background(), backend.OpenRequest{
		Project: projects.Project{
			ID: "alpha", Path: nativePath,
			WindowsWSL: &projects.WindowsWSL{Distro: "Ubuntu", WSLPath: "/home/me/alpha"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []string{"wsl.exe", "-d", "Ubuntu", "--cd", "/home/me/alpha"}
	if !reflect.DeepEqual(executor.request.Args[len(executor.request.Args)-len(wantTail):], wantTail) {
		t.Fatalf("overlay was not preserved: %v", executor.request.Args)
	}
}

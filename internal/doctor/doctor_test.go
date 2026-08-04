package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
)

type fakeExecutor struct {
	paths map[string]string
}

func (executor fakeExecutor) LookPath(name string) (string, error) {
	if path := executor.paths[name]; path != "" {
		return path, nil
	}
	return "", fmt.Errorf("missing %s", name)
}

func (fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	return backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0, Stdout: request.Name + " 1.0\n"}, nil
}

type fakeAdapter struct {
	name       backend.Name
	available  bool
	version    string
	reason     string
	operations []string
}

func (adapter fakeAdapter) Name() backend.Name { return adapter.name }
func (adapter fakeAdapter) Detect(context.Context, backend.OpenRequest) backend.Capability {
	return backend.Capability{
		Backend: adapter.name, Available: adapter.available, Version: adapter.version,
		Reason: adapter.reason, Capabilities: adapter.operations,
	}
}
func (adapter fakeAdapter) OpenProject(context.Context, backend.OpenRequest) (backend.OpenResult, error) {
	return backend.OpenResult{Backend: adapter.name}, nil
}

func testPaths(root string) config.Paths {
	return config.Paths{
		ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		ConfigFile: filepath.Join(root, "config", "config.toml"), ProjectsFile: filepath.Join(root, "config", "projects.toml"),
		ProfilesDir: filepath.Join(root, "config", "profiles"), BackupsDir: filepath.Join(root, "state", "backups"),
		AgentsFile: filepath.Join(root, "state", "agents.json"), WorktreesFile: filepath.Join(root, "state", "worktrees.json"),
	}
}

func testRegistry(environment backend.Environment) *backend.Registry {
	return backend.NewRegistry(environment,
		fakeAdapter{name: backend.Shell, available: true, version: "/bin/sh", operations: []string{"open_project"}},
		fakeAdapter{name: backend.Tmux, reason: "not installed"},
		fakeAdapter{name: backend.CMUX, reason: "unsupported"},
		fakeAdapter{name: backend.WindowsTerminal, reason: "unsupported"},
	)
}

func capability(report Report, name string) Capability {
	for _, item := range report.Capabilities {
		if item.Name == name {
			return item
		}
	}
	return Capability{}
}

func TestDoctorDistinguishesOptionalAndPlatformSkippedCapabilities(t *testing.T) {
	root := t.TempDir()
	environment := backend.Environment{GOOS: "linux", Getenv: func(string) string { return "" }}
	report := NewManager(testPaths(root), fakeExecutor{paths: map[string]string{"git": "/usr/bin/git"}}, environment, testRegistry(environment)).Run(context.Background(), "")
	if !report.Healthy(false) || report.Healthy(true) {
		t.Fatalf("optional capabilities should fail only in strict mode: %#v", report.Summary)
	}
	if item := capability(report, "backend:cmux"); item.Scope != Disabled || item.Status != Skipped {
		t.Fatalf("cmux should be skipped on Linux: %#v", item)
	}
	if item := capability(report, "backend:windows-terminal"); item.Scope != Disabled || item.Status != Skipped {
		t.Fatalf("Windows Terminal should be skipped outside Windows/WSL: %#v", item)
	}
	if capability(report, "git").Version == "" {
		t.Fatal("available tool version was not retained")
	}
}

func TestDoctorReportsInvalidAgentRegistryAsCoreFailure(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgentsFile, []byte(`{"schema_version":99,"tasks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := backend.Environment{GOOS: "linux", Getenv: func(string) string { return "" }}
	report := NewManager(paths, fakeExecutor{paths: map[string]string{"git": "/usr/bin/git"}}, environment, testRegistry(environment)).Run(context.Background(), "")
	if report.Healthy(false) || capability(report, "agents-state").Scope != Core || capability(report, "agents-state").Status != Unavailable {
		t.Fatalf("invalid Agent state was not a core failure: %#v", capability(report, "agents-state"))
	}
}

func TestDoctorTreatsWindowsTerminalAsOptionalInsideWSL(t *testing.T) {
	root := t.TempDir()
	environment := backend.Environment{GOOS: "linux", Getenv: func(key string) string {
		if key == "WSL_DISTRO_NAME" {
			return "Ubuntu"
		}
		return ""
	}}
	report := NewManager(testPaths(root), fakeExecutor{paths: map[string]string{"git": "/usr/bin/git"}}, environment, testRegistry(environment)).Run(context.Background(), "")
	item := capability(report, "backend:windows-terminal")
	if report.Platform != "windows-wsl" || item.Scope != Optional || item.Status != Unavailable {
		t.Fatalf("unexpected WSL capability: platform=%s item=%#v", report.Platform, item)
	}
}

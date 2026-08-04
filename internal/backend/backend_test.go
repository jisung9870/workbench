package backend

import (
	"context"
	"testing"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type stubAdapter struct {
	name      Name
	available bool
	reason    string
	detects   int
}

func (adapter *stubAdapter) Name() Name { return adapter.name }

func (adapter *stubAdapter) Detect(context.Context, OpenRequest) Capability {
	adapter.detects++
	return Capability{Backend: adapter.name, Available: adapter.available, Reason: adapter.reason}
}

func (adapter *stubAdapter) OpenProject(context.Context, OpenRequest) (OpenResult, error) {
	return OpenResult{Backend: adapter.name}, nil
}

func selectionRequest() OpenRequest {
	return OpenRequest{
		Project: projects.Project{ID: "alpha", DefaultBackend: "auto"},
		Profile: config.DefaultProfile(),
	}
}

func TestExplicitBackendWinsEvenOverSSHEnvironment(t *testing.T) {
	cmux := &stubAdapter{name: CMUX, available: true}
	tmux := &stubAdapter{name: Tmux, available: true}
	shell := &stubAdapter{name: Shell, available: true}
	registry := NewRegistry(Environment{GOOS: "darwin", Getenv: func(key string) string {
		if key == "SSH_CONNECTION" {
			return "client server"
		}
		return ""
	}}, cmux, tmux, shell)
	selection, err := registry.Select(context.Background(), selectionRequest(), CMUX)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != CMUX {
		t.Fatalf("explicit backend lost priority: %s", selection.Adapter.Name())
	}
}

func TestSSHSkipsCMUXAndChoosesTmux(t *testing.T) {
	cmux := &stubAdapter{name: CMUX, available: true}
	tmux := &stubAdapter{name: Tmux, available: true}
	shell := &stubAdapter{name: Shell, available: true}
	registry := NewRegistry(Environment{GOOS: "darwin", Getenv: func(key string) string {
		if key == "SSH_TTY" {
			return "/dev/pts/1"
		}
		return ""
	}}, cmux, tmux, shell)
	selection, err := registry.Select(context.Background(), selectionRequest(), Auto)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != Tmux {
		t.Fatalf("expected tmux, got %s", selection.Adapter.Name())
	}
	if cmux.detects != 0 {
		t.Fatal("cmux was probed during SSH auto-selection")
	}
}

func TestConfiguredUnavailableBackendFallsBackWithWarning(t *testing.T) {
	cmux := &stubAdapter{name: CMUX, available: false, reason: "not installed"}
	shell := &stubAdapter{name: Shell, available: true}
	request := selectionRequest()
	request.Project.DefaultBackend = "cmux"
	registry := NewRegistry(Environment{GOOS: "linux", Getenv: func(string) string { return "" }}, cmux, shell)
	selection, err := registry.Select(context.Background(), request, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != Shell || len(selection.Warnings) != 1 {
		t.Fatalf("unexpected selection: %s warnings=%v", selection.Adapter.Name(), selection.Warnings)
	}
}

func TestNativeWindowsAutoSelectsWindowsTerminal(t *testing.T) {
	terminal := &stubAdapter{name: WindowsTerminal, available: true}
	shell := &stubAdapter{name: Shell, available: true}
	registry := NewRegistry(Environment{GOOS: "windows", Getenv: func(string) string { return "" }}, terminal, shell)
	selection, err := registry.Select(context.Background(), selectionRequest(), Auto)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter.Name() != WindowsTerminal {
		t.Fatalf("expected Windows Terminal, got %s", selection.Adapter.Name())
	}
}

func TestExplicitUnavailableReturnsRecoveryFallbacks(t *testing.T) {
	cmux := &stubAdapter{name: CMUX, available: false, reason: "unsupported"}
	shell := &stubAdapter{name: Shell, available: true}
	registry := NewRegistry(Environment{GOOS: "linux", Getenv: func(string) string { return "" }}, cmux, shell)
	_, err := registry.Select(context.Background(), selectionRequest(), CMUX)
	unavailable, ok := err.(*UnavailableError)
	if !ok {
		t.Fatalf("expected UnavailableError, got %T (%v)", err, err)
	}
	if len(unavailable.Fallback) != 1 || unavailable.Fallback[0] != Shell {
		t.Fatalf("unexpected fallbacks: %v", unavailable.Fallback)
	}
}

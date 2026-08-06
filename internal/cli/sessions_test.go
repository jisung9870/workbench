package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/sessions"
)

func TestSessionsRejectInvalidArgumentsBeforeTmuxLookup(t *testing.T) {
	tests := [][]string{
		{"sessions"},
		{"sessions", "list", "extra"},
		{"sessions", "show"},
		{"sessions", "jump", "--json"},
		{"sessions", "adopt"},
		{"sessions", "stop"},
		{"sessions", "unknown"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitArgument {
			t.Fatalf("expected argument error for %v, got %d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestSessionErrorExitContracts(t *testing.T) {
	tests := []struct {
		err  error
		exit int
		code string
	}{
		{err: &sessions.NotFoundError{Name: "alpha"}, exit: ExitGeneral, code: "SESSION_NOT_FOUND"},
		{err: &sessions.ConflictError{Message: "unsafe"}, exit: ExitConflict, code: "SESSION_CONFLICT"},
		{err: &backend.UnavailableError{Backend: backend.Tmux, Reason: "missing"}, exit: ExitUnavailable, code: "BACKEND_UNAVAILABLE"},
		{err: errors.New("failed"), exit: ExitGeneral, code: "OPERATION_FAILED"},
	}
	for _, test := range tests {
		got := sessionError(test.err)
		if got.ExitCode != test.exit || got.Code != test.code {
			t.Fatalf("unexpected command error for %T: %#v", test.err, got)
		}
	}
}

func TestSessionProjectReportsMissingRegistryEntry(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		ConfigDir:    filepath.Join(root, "config"),
		StateDir:     root,
		ProjectsFile: filepath.Join(root, "projects.toml"),
		BackupsDir:   filepath.Join(root, "backups"),
	}
	_, err := sessionProject(paths, "alpha")
	if err == nil || err.Code != "PROJECT_NOT_FOUND" || err.ExitCode != ExitGeneral {
		t.Fatalf("unexpected project error: %#v", err)
	}
}

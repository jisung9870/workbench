package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/output"
)

func TestProjectsJSONEnvelopeAndInvalidArgument(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	projectDir := filepath.Join(root, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"projects", "add", projectDir}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add failed: code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"projects", "list", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("list failed: code=%d stderr=%s", code, stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%s)", err, stdout.String())
	}
	if !envelope.OK || envelope.SchemaVersion != 1 || stderr.Len() != 0 {
		t.Fatalf("unexpected envelope or diagnostics: %#v stderr=%s", envelope, stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"projects", "list", "--json", "extra"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("expected argument exit, got %d", code)
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("unexpected error envelope: %#v %v", envelope, err)
	}
}

func TestOpenExplicitUnavailableUsesExitThree(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("cmux may be available on macOS")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	projectDir := filepath.Join(root, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"projects", "add", projectDir}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add failed: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"open", "alpha", "--backend", "cmux"}, &stdout, &stderr); code != ExitUnavailable {
		t.Fatalf("expected exit 3, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "backend \"cmux\" is unavailable") {
		t.Fatalf("missing recovery error: %s", stderr.String())
	}
}

func TestOpenShellPreservesBackendExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("WSL_INTEROP", "")
	t.Setenv("WSL_DISTRO_NAME", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("SHELL", "/bin/false")
	projectDir := filepath.Join(root, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"projects", "add", projectDir}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add failed: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"open", "alpha", "--backend", "shell"}, &stdout, &stderr); code != ExitGeneral {
		t.Fatalf("expected backend failure, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "shell:alpha") {
		t.Fatalf("backend reference was lost: %s", stderr.String())
	}
}

func TestConfirmBranchRequiresExactName(t *testing.T) {
	var output bytes.Buffer
	if confirmBranch(strings.NewReader("wrong\n"), &output, "feature/delete") {
		t.Fatal("wrong branch confirmation was accepted")
	}
	output.Reset()
	if !confirmBranch(strings.NewReader("feature/delete\n"), &output, "feature/delete") {
		t.Fatal("exact branch confirmation was rejected")
	}
}

func TestAgentsListJSONUsesRegistryStateSource(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	stateRoot := filepath.Join(root, "state")
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("APPDATA", configRoot)
	t.Setenv("LOCALAPPDATA", stateRoot)
	paths := config.Paths{
		StateDir:   filepath.Join(stateRoot, "workbench"),
		AgentsFile: filepath.Join(stateRoot, "workbench", "agents.json"),
		BackupsDir: filepath.Join(stateRoot, "workbench", "backups"),
	}
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	_, err := agents.NewStateStore(paths).Create(agents.Task{
		ID: "task-cli", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux,
		BackendRef: "tmux:%2", BackendDetails: map[string]string{"pane": "%2", "session": "alpha"},
		State: agents.Completed, StateSource: agents.SourceRegistry, CWD: root,
		StartedAt: now, LastEventAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents", "list", "--project", "alpha", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("agents list failed: code=%d stderr=%s", code, stderr.String())
	}
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
		OK            bool
		Data          struct {
			Agents []agents.Task `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON envelope: %v (%s)", err, stdout.String())
	}
	if !envelope.OK || len(envelope.Data.Agents) != 1 || envelope.Data.Agents[0].StateSource != agents.SourceRegistry {
		t.Fatalf("unexpected agent envelope: %#v", envelope)
	}
}

func TestAgentsStopUnknownTaskDoesNotTouchBackend(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents", "stop", "task-guessed"}, &stdout, &stderr); code != ExitGeneral {
		t.Fatalf("expected not-found exit without backend action, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "was not found") {
		t.Fatalf("missing safe not-found diagnostic: %s", stderr.String())
	}
}

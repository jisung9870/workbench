package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

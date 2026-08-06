package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/compatibility"
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

func TestEnvironmentCRUDExportAndJSONContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	args := []string{"env", "add", "dev", "--aws-profile", "sandbox", "--aws-region", "ap-northeast-2", "--kube-context", "cluster", "--set", "FEATURE=hello world", "--json"}
	if code := Run(args, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK || !strings.Contains(stdout.String(), `"environment":{"id":"dev"`) {
		t.Fatalf("unexpected add envelope: %#v err=%v output=%s", envelope, err, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "export", "dev"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("export failed: %d %s", code, stderr.String())
	}
	if stdout.String() != "export AWS_PROFILE='sandbox'\nexport AWS_REGION='ap-northeast-2'\nexport FEATURE='hello world'\n" {
		t.Fatalf("unsafe or unexpected shell output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "kube context/namespace mutation is not implemented") {
		t.Fatalf("missing kube warning: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "list", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("list failed: %d %s", code, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK || !strings.Contains(stdout.String(), `"environments"`) {
		t.Fatalf("unexpected list envelope: %#v err=%v", envelope, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "remove", "dev", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("remove failed: %d %s", code, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK {
		t.Fatalf("unexpected remove envelope: %#v err=%v", envelope, err)
	}
}

func TestEnvironmentSecretReferencesHealthAndExplicitExport(t *testing.T) {
	const sentinel = "ENV-SECRET-'SENTINEL"
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := RunWithInput([]string{"secrets", "init"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("init=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "set", "service", "token"}, strings.NewReader(sentinel), &stdout, &stderr); code != ExitOK {
		t.Fatalf("set=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "add", "dev", "--set", "FEATURE=on", "--secret", "TOKEN=sec://service/token", "--secret", "MISSING=sec://service/missing", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), sentinel) || !strings.Contains(stdout.String(), `"TOKEN":"sec://service/token"`) {
		t.Fatalf("unsafe add output stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "health", "dev", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("health=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, sentinel) || !strings.Contains(stdout.String(), `"reason":"missing"`) || !strings.Contains(stdout.String(), `"available":true`) {
		t.Fatalf("unsafe health output stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "export", "dev"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("plain export=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), sentinel) || strings.Contains(stdout.String(), "TOKEN") || !strings.Contains(stderr.String(), "secret references were not resolved") {
		t.Fatalf("unsafe plain export stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "export", "dev", "--resolve-secrets"}, &stdout, &stderr); code != ExitUnavailable {
		t.Fatalf("missing resolve=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), sentinel) {
		t.Fatalf("failed resolution leaked value stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "remove", "dev"}, &stdout, &stderr); code != ExitOK {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "add", "ready", "--set", "FEATURE=on", "--secret", "TOKEN=sec://service/token"}, &stdout, &stderr); code != ExitOK {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "export", "ready", "--resolve-secrets"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("resolve=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	expected := "export FEATURE='on'\nexport TOKEN='ENV-SECRET-'\\''SENTINEL'\n"
	if stdout.String() != expected {
		t.Fatalf("resolved shell output=%q", stdout.String())
	}
}

func TestEnvironmentSecretReferenceArgumentsAndTerminalGuard(t *testing.T) {
	const malformedSentinel = "MALFORMED-REFERENCE-SENTINEL"
	for _, args := range [][]string{
		{"env", "add", "dev", "--secret", "TOKEN=" + malformedSentinel},
		{"env", "add", "dev", "--secret", "TOKEN=sec://service/token", "--set", "TOKEN=plain"},
		{"env", "add", "dev", "--set", "TOKEN=plain", "--secret", "TOKEN=sec://service/token"},
		{"env", "add", "dev", "--secret", "TOKEN=sec://service/token", "--secret", "TOKEN=sec://service/other"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitArgument {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), malformedSentinel) {
			t.Fatalf("malformed reference value leaked: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
	}
	if err := validateEnvExportOptions(true, true, false); err == nil {
		t.Fatal("expected JSON and secret resolution conflict")
	}
	if err := validateEnvExportOptions(false, true, true); err == nil || !strings.Contains(err.Message, "terminal") {
		t.Fatalf("expected terminal refusal, got %#v", err)
	}
}

func TestEnvironmentHealthSanitizesBrokenIdentity(t *testing.T) {
	const identitySentinel = "AGE-SECRET-KEY-1-RAW-IDENTITY-SENTINEL"
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"env", "add", "dev", "--secret", "TOKEN=sec://service/token"}, &stdout, &stderr); code != ExitOK {
		t.Fatal(stderr.String())
	}
	identityPath := filepath.Join(root, "config", "workbench", "age.key")
	if err := os.WriteFile(identityPath, []byte(identitySentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "health", "dev", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("health=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), identitySentinel) || !strings.Contains(stdout.String(), `"reason":"store_unavailable"`) {
		t.Fatalf("identity leaked or reason missing stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestEnvironmentResolvedExportRejectsNULWithoutOutputOrLeak(t *testing.T) {
	const sentinel = "NUL-SECRET-SENTINEL"
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := RunWithInput([]string{"secrets", "init"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("init=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "set", "service", "nul"}, strings.NewReader(sentinel+"\x00tail"), &stdout, &stderr); code != ExitOK {
		t.Fatalf("set=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "add", "dev", "--set", "FIRST=must-not-be-partially-written", "--secret", "TOKEN=sec://service/nul"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "export", "dev", "--resolve-secrets"}, &stdout, &stderr); code != ExitGeneral {
		t.Fatalf("export=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("NUL rejection wrote partial stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), sentinel) || strings.Contains(stderr.String(), "tail") || !strings.Contains(stderr.String(), "contains NUL") {
		t.Fatalf("unsafe or unclear error: %q", stderr.String())
	}
}

func TestSecretsCLIJSONIsMetadataOnlyAndGetRejectsJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := RunWithInput([]string{"secrets", "init", "--json"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("init code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	identityPath := filepath.Join(root, "config", "workbench", "age.key")
	identity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stdout.Bytes(), bytes.TrimSpace(identity)) {
		t.Fatalf("identity leaked: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	const sentinel = "CLI-SECRET-SENTINEL\nline-two"
	if code := RunWithInput([]string{"secrets", "set", "svc", "token", "--json"}, strings.NewReader(sentinel), &stdout, &stderr); code != ExitOK {
		t.Fatalf("set code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), sentinel) || strings.Contains(stderr.String(), sentinel) {
		t.Fatalf("set leaked value stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK || envelope.SchemaVersion != 1 {
		t.Fatalf("set envelope=%#v err=%v", envelope, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "list", "--json"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("list code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), sentinel) || !strings.Contains(stdout.String(), `"service":"svc"`) || !strings.Contains(stdout.String(), `"field":"token"`) {
		t.Fatalf("unsafe list output: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "get", "svc", "token", "--json"}, strings.NewReader(""), &stdout, &stderr); code != ExitArgument {
		t.Fatalf("get --json code=%d", code)
	}
	if strings.Contains(stdout.String(), sentinel) || strings.Contains(stderr.String(), sentinel) {
		t.Fatal("get --json error leaked value")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "get", "svc", "token"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("get code=%d stderr=%s", code, stderr.String())
	}
	if stdout.String() != sentinel+"\n" {
		t.Fatalf("get=%q", stdout.String())
	}
}

func TestSecretsCLIRejectsArgvValueAndOverwriteWithoutLeak(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := RunWithInput([]string{"secrets", "init"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("init=%d %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "set", "svc", "field", "ARGV-SENTINEL"}, strings.NewReader(""), &stdout, &stderr); code != ExitArgument {
		t.Fatalf("argv value code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "set", "svc", "field"}, strings.NewReader("FIRST-SENTINEL"), &stdout, &stderr); code != ExitOK {
		t.Fatalf("first set=%d %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "set", "svc", "field", "--json"}, strings.NewReader("REPLACEMENT-SENTINEL"), &stdout, &stderr); code != ExitConflict {
		t.Fatalf("overwrite code=%d", code)
	}
	if strings.Contains(stdout.String(), "REPLACEMENT-SENTINEL") || strings.Contains(stderr.String(), "REPLACEMENT-SENTINEL") {
		t.Fatal("overwrite error leaked proposed value")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "set", "svc", "field", "--replace", "--json"}, strings.NewReader("REPLACEMENT-SENTINEL"), &stdout, &stderr); code != ExitOK {
		t.Fatalf("replace code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "REPLACEMENT-SENTINEL") || strings.Contains(stderr.String(), "REPLACEMENT-SENTINEL") || !strings.Contains(stdout.String(), `"replace_requested":true`) {
		t.Fatalf("unsafe replace output=%s stderr=%s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "remove", "svc", "field"}, strings.NewReader("y\n"), &stdout, &stderr); code != ExitArgument {
		t.Fatalf("noninteractive remove code=%d", code)
	}
	if !strings.Contains(stderr.String(), "requires --yes") {
		t.Fatalf("missing noninteractive guard: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunWithInput([]string{"secrets", "remove", "svc", "field", "--yes", "--json"}, strings.NewReader(""), &stdout, &stderr); code != ExitOK {
		t.Fatalf("approved remove code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "REPLACEMENT-SENTINEL") || !strings.Contains(stdout.String(), `"removed":true`) {
		t.Fatalf("unsafe remove output: %s", stdout.String())
	}
}

func TestSecretRemovalInteractiveConfirmation(t *testing.T) {
	var stderr bytes.Buffer
	confirmed, err := confirmSecretRemoval(strings.NewReader("n\n"), &stderr, "svc/token", false, true)
	if err != nil || confirmed || !strings.Contains(stderr.String(), "[y/N]") {
		t.Fatalf("cancel confirmed=%t err=%v prompt=%s", confirmed, err, stderr.String())
	}
	stderr.Reset()
	confirmed, err = confirmSecretRemoval(strings.NewReader("YES\n"), &stderr, "svc/token", false, true)
	if err != nil || !confirmed {
		t.Fatalf("approval confirmed=%t err=%v", confirmed, err)
	}
	confirmed, err = confirmSecretRemoval(strings.NewReader(""), &stderr, "svc/token", true, false)
	if err != nil || !confirmed {
		t.Fatalf("--yes confirmed=%t err=%v", confirmed, err)
	}
}

func TestEnvironmentMigrationCheckReadOnlyAndBlockedApplyJSON(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "wenv.d")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "dev"), []byte("AWS_PROFILE=migration-value-must-not-be-echoed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"env", "migrate", "check", "--source", source, "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("check failed: %d %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), `"applied":true`) || !strings.Contains(stdout.String(), `"can_apply":true`) {
		t.Fatalf("unexpected check result: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "migration-value-must-not-be-echoed") {
		t.Fatalf("migration check exposed preset values: %s", stdout.String())
	}
	registry := filepath.Join(root, "config", "workbench", "environments.toml")
	if _, err := os.Stat(registry); !os.IsNotExist(err) {
		t.Fatalf("check wrote registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "unsafe"), []byte("source ~/.profile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"env", "migrate", "apply", "--source", source, "--json"}, &stdout, &stderr); code != ExitConflict {
		t.Fatalf("expected blocked apply, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != "ENV_MIGRATION_BLOCKED" {
		t.Fatalf("unexpected blocked envelope: %#v err=%v output=%s", envelope, err, stdout.String())
	}
	if _, err := os.Stat(registry); !os.IsNotExist(err) {
		t.Fatalf("blocked apply wrote registry: %v", err)
	}
}

func TestEnvironmentMigrationCheckDoesNotExposeInvalidExportValues(t *testing.T) {
	const sentinel = "SENTINEL_SECRET_VALUE"
	root := t.TempDir()
	source := filepath.Join(root, "wenv.d")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "unsafe"), []byte("EXPORTS=(BAD-NAME="+sentinel+")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	for _, jsonMode := range []bool{false, true} {
		args := []string{"env", "migrate", "check", "--source", source}
		if jsonMode {
			args = append(args, "--json")
		}
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitOK {
			t.Fatalf("check failed: mode=%v code=%d stdout=%s stderr=%s", jsonMode, code, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if strings.Contains(combined, sentinel) {
			t.Fatalf("migration check exposed preset value: mode=%v output=%s", jsonMode, combined)
		}
		if !strings.Contains(combined, "invalid EXPORTS item at index 0") {
			t.Fatalf("sanitized location error missing: mode=%v output=%s", jsonMode, combined)
		}
	}
}

func TestWorkflowCatalogJSONAndDisallowedID(t *testing.T) {
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
	if code := Run([]string{"projects", "add", projectDir, "--id", "alpha"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("add failed: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"workflows", "catalog", "--project", "alpha", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("catalog failed: code=%d stderr=%s", code, stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK || envelope.SchemaVersion != 1 || !strings.Contains(stdout.String(), `"workflows"`) {
		t.Fatalf("unexpected workflow envelope: %#v err=%v output=%s", envelope, err, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"workflows", "run", "shell", "--project", "alpha", "--json"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("disallowed workflow exit=%d output=%s", code, stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("unexpected disallowed envelope: %#v err=%v", envelope, err)
	}
}

func TestSessionsJSONTreatsMissingTmuxAsOptionalUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runSessions([]string{"list", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("optional tmux observation failed: %v", err)
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("sessions output is not JSON: %v (%s)", err, stdout.String())
	}
	if !envelope.OK || !strings.Contains(stdout.String(), `"available":false`) || !strings.Contains(stdout.String(), "tmux executable was not found") || stderr.Len() != 0 {
		t.Fatalf("unexpected optional-unavailable envelope: %s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestOverviewJSONSucceedsWhenTmuxAndBinboxAreUnavailable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"overview", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("optional providers failed overview: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK {
		t.Fatalf("overview output is not a successful envelope: %#v err=%v output=%s", envelope, err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"tool_health":{"provider":"binbox","available":false`) || !strings.Contains(stdout.String(), `"tmux:unavailable"`) {
		t.Fatalf("optional-unavailable state is missing: %s", stdout.String())
	}
}

func TestTasksStopRefusesObservedOwnership(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runTasks([]string{"stop", "tmux:%12"}, config.Paths{}, &stdout, &stderr)
	if err == nil || err.ExitCode != ExitConflict || err.Code != "TASK_UNMANAGED" {
		t.Fatalf("observed task stop was not refused: %#v", err)
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
	falseCommand, err := exec.LookPath("false")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", falseCommand)
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

func TestOpenRejectsWindowsTerminalOptionsForShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
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
	if code := Run([]string{"open", "alpha", "--backend", "shell", "--window", "new"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("expected argument error, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "require the Windows Terminal backend") {
		t.Fatalf("missing backend constraint: %s", stderr.String())
	}
}

func TestOpenRejectsInvalidWindowsTerminalMode(t *testing.T) {
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
	if code := Run([]string{"open", "alpha", "--terminal-mode", "diagonal"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("expected argument error, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid Windows Terminal mode") {
		t.Fatalf("missing validation error: %s", stderr.String())
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
	observations, err := compatibility.NewStore(filepath.Join(stateRoot, "workbench", "compatibility")).Load()
	if err != nil || len(observations) != 1 || observations[0].Client != "workbench" || observations[0].Source != "registry" {
		t.Fatalf("Agent registry use was not observed: %#v err=%v", observations, err)
	}
}

func TestCompatibilityObserveAllowsOnlyExternalTuples(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	valid := []string{"compatibility", "observe", "--client", "nvim", "--feature", "projects", "--source", "binbox"}
	if code := Run(valid, &stdout, &stderr); code != ExitOK {
		t.Fatalf("valid observation failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	store := compatibility.NewStore(filepath.Join(root, "state", "workbench", "compatibility"))
	observations, err := store.Load()
	if err != nil || len(observations) != 1 || observations[0].Source != "binbox" {
		t.Fatalf("observation was not stored: %#v err=%v", observations, err)
	}
	stdout.Reset()
	stderr.Reset()
	internal := []string{"compatibility", "observe", "--client", "workbench", "--feature", "agents", "--source", "registry"}
	if code := Run(internal, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("internal tuple was accepted: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
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

func TestDoctorJSONPreservesDataAndStrictFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	stateRoot := filepath.Join(root, "state")
	binRoot := filepath.Join(root, "bin")
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(binRoot, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version fixture\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("APPDATA", configRoot)
	t.Setenv("LOCALAPPDATA", stateRoot)
	t.Setenv("PATH", binRoot)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("WSL_INTEROP", "")
	t.Setenv("WSL_DISTRO_NAME", "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("default doctor should tolerate optional misses: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || !envelope.OK || envelope.Data == nil || len(envelope.Warnings) == 0 {
		t.Fatalf("unexpected doctor success envelope: %#v err=%v", envelope, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"doctor", "--json", "--strict"}, &stdout, &stderr); code != ExitGeneral {
		t.Fatalf("strict doctor should fail on optional misses: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.OK || envelope.Data == nil || envelope.Error == nil || envelope.Error.Code != "OPTIONAL_CAPABILITY_UNAVAILABLE" {
		t.Fatalf("strict failure lost collected data: %#v err=%v", envelope, err)
	}
}

func TestDoctorInvalidStateIsCoreFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	stateRoot := filepath.Join(root, "state")
	binRoot := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(stateRoot, "workbench"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binRoot, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "workbench", "agents.json"), []byte(`{"schema_version":99,"tasks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("APPDATA", configRoot)
	t.Setenv("LOCALAPPDATA", stateRoot)
	t.Setenv("PATH", binRoot)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("WSL_INTEROP", "")
	t.Setenv("WSL_DISTRO_NAME", "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--json"}, &stdout, &stderr); code != ExitGeneral {
		t.Fatalf("invalid state should fail doctor: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.Error == nil || envelope.Error.Code != "CORE_CAPABILITY_UNAVAILABLE" || envelope.Data == nil {
		t.Fatalf("core failure envelope lost diagnostics: %#v err=%v", envelope, err)
	}
}

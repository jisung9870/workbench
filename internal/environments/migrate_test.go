package environments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWenvSupportedSubset(t *testing.T) {
	preset := `# comment
AWS_PROFILE='dev profile'
AWS_REGION=ap-northeast-2 # inline comment
KUBE_CONTEXT="local-cluster"
KUBE_NAMESPACE=tools
EXPORTS=(
  FEATURE=on
  "MESSAGE=hello world"
)
`
	environment, err := ParseWenv("dev", strings.NewReader(preset))
	if err != nil {
		t.Fatal(err)
	}
	if environment.AWSProfile != "dev profile" || environment.AWSRegion != "ap-northeast-2" || environment.KubeContext != "local-cluster" || environment.KubeNamespace != "tools" || environment.Exports["MESSAGE"] != "hello world" {
		t.Fatalf("unexpected environment: %#v", environment)
	}
}

func TestParseWenvRejectsShellExecutionAndExpansion(t *testing.T) {
	cases := []string{
		"source ~/.profile\nAWS_PROFILE=dev\n",
		"AWS_PROFILE=$(touch /tmp/nope)\n",
		"AWS_PROFILE=\"$USER\"\n",
		"EXPORTS=(TOKEN=`whoami`)\n",
		"AWS_PROFILE=dev\nAWS_PROFILE=prod\n",
	}
	for _, preset := range cases {
		if _, err := ParseWenv("dev", strings.NewReader(preset)); err == nil {
			t.Fatalf("expected unsupported syntax for %q", preset)
		}
	}
}

func TestInvalidExportErrorsDoNotExposeValues(t *testing.T) {
	const sentinel = "SENTINEL_SECRET_VALUE"
	for _, preset := range []string{
		"EXPORTS=(BAD-NAME=" + sentinel + ")\n",
		"EXPORTS=(MALFORMED_" + sentinel + ")\n",
	} {
		_, err := ParseWenv("dev", strings.NewReader(preset))
		if err == nil {
			t.Fatalf("expected invalid EXPORTS item for %q", preset)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("parser error exposed preset value: %v", err)
		}
	}
}

func TestMigrationPlanIssuesDoNotExposeInvalidExportValues(t *testing.T) {
	const sentinel = "SENTINEL_SECRET_VALUE"
	root := t.TempDir()
	source := filepath.Join(root, "wenv.d")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "unsafe"), []byte("EXPORTS=(BAD-NAME="+sentinel+")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanWenv(source, NewStore(testPaths(root)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || len(plan.Items) != 1 || plan.Items[0].Status != MigrationUnsupported {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if strings.Contains(strings.Join(plan.Items[0].Issues, "\n"), sentinel) {
		t.Fatalf("migration plan exposed preset value: %#v", plan.Items[0].Issues)
	}
}

func TestMigrationCheckAndApplyAreAllOrNothing(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "wenv.d")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "dev"), []byte("AWS_PROFILE=dev\nEXPORTS=(FEATURE=on)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(root)
	store := NewStore(paths)
	plan, err := PlanWenv(source, store)
	if err != nil || !plan.CanApply || plan.Ready != 1 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := os.Stat(paths.EnvironmentsFile); !os.IsNotExist(err) {
		t.Fatalf("check changed registry: %v", err)
	}
	if _, err := ApplyWenv(plan, store); err != nil {
		t.Fatal(err)
	}
	item, found, err := store.Show("dev")
	if err != nil || !found || item.AWSProfile != "dev" {
		t.Fatalf("item=%#v found=%v err=%v", item, found, err)
	}
	plan, err = PlanWenv(source, store)
	if err != nil || !plan.CanApply || plan.Existing != 1 || plan.Ready != 0 {
		t.Fatalf("idempotent plan=%#v err=%v", plan, err)
	}
	marker := filepath.Join(root, "must-not-exist")
	if err := os.WriteFile(filepath.Join(source, "unsafe"), []byte("AWS_PROFILE=$(touch "+marker+")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.EnvironmentsFile)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := PlanWenv(source, store)
	if err != nil || blocked.CanApply || blocked.Blocked != 1 {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
	if _, err := ApplyWenv(blocked, store); err == nil {
		t.Fatal("expected blocked apply")
	}
	after, err := os.ReadFile(paths.EnvironmentsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("blocked apply changed registry")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("unsupported command was executed")
	}
}

func TestMigrationConflictPreservesExisting(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "wenv.d")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(root)
	store := NewStore(paths)
	if _, err := store.Add(Environment{ID: "dev", AWSProfile: "original", Exports: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "dev"), []byte("AWS_PROFILE=replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(paths.EnvironmentsFile)
	plan, err := PlanWenv(source, store)
	if err != nil || plan.CanApply || plan.Items[0].Status != MigrationConflict {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := ApplyWenv(plan, store); err == nil {
		t.Fatal("expected conflict")
	}
	after, _ := os.ReadFile(paths.EnvironmentsFile)
	if string(before) != string(after) {
		t.Fatal("conflict changed registry")
	}
}

func TestApplyRejectsSourceChangedAfterCheck(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "wenv.d")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	preset := filepath.Join(source, "dev")
	if err := os.WriteFile(preset, []byte("AWS_PROFILE=first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(testPaths(root))
	plan, err := PlanWenv(source, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preset, []byte("AWS_PROFILE=second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWenv(plan, store); err == nil {
		t.Fatal("expected changed source conflict")
	}
	items, err := store.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("changed source was applied: %#v err=%v", items, err)
	}
}

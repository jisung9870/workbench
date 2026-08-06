package environments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/config"
)

func testPaths(root string) config.Paths {
	return config.Paths{
		ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		EnvironmentsFile: filepath.Join(root, "config", "environments.toml"),
		BackupsDir:       filepath.Join(root, "state", "backups"),
	}
}

func TestStoreCRUDBackupsAndPermissions(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	first := Environment{ID: "dev", AWSProfile: "sandbox", Exports: map[string]string{"FEATURE": "on"}}
	backup, err := store.Add(first)
	if err != nil || backup != "" {
		t.Fatalf("first add: backup=%q err=%v", backup, err)
	}
	info, err := os.Stat(paths.EnvironmentsFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry permissions = %o", info.Mode().Perm())
	}
	backup, err = store.Add(Environment{ID: "prod", AWSRegion: "ap-northeast-2", Exports: map[string]string{}})
	if err != nil || backup == "" {
		t.Fatalf("second add: backup=%q err=%v", backup, err)
	}
	if backupInfo, statErr := os.Stat(backup); statErr != nil || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions: info=%v err=%v", backupInfo, statErr)
	}
	items, err := store.List()
	if err != nil || len(items) != 2 || items[0].ID != "dev" || items[1].ID != "prod" {
		t.Fatalf("list=%#v err=%v", items, err)
	}
	removed, found, backup, err := store.Remove("dev")
	if err != nil || !found || removed.ID != "dev" || backup == "" {
		t.Fatalf("remove=%#v found=%v backup=%q err=%v", removed, found, backup, err)
	}
}

func TestStoreRejectsConflictWithoutChangingRegistry(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	if _, err := store.Add(Environment{ID: "dev", Exports: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.EnvironmentsFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Environment{ID: "dev", AWSProfile: "other", Exports: map[string]string{}}); err == nil {
		t.Fatal("expected conflict")
	}
	after, err := os.ReadFile(paths.EnvironmentsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("registry changed after conflict")
	}
}

func TestShellExportsQuotesValuesAndOmitsKubeMutation(t *testing.T) {
	result := ShellExports(Environment{AWSProfile: "dev's profile", KubeContext: "cluster", Exports: map[string]string{"ZED": "two words"}})
	if result != "export AWS_PROFILE='dev'\\''s profile'\nexport ZED='two words'\n" {
		t.Fatalf("unexpected exports: %q", result)
	}
	if strings.Contains(result, "KUBE") {
		t.Fatalf("kube mutation leaked into shell output: %s", result)
	}
}

func TestValidateRejectsReservedAndInvalidExportKeys(t *testing.T) {
	for _, key := range []string{"AWS_PROFILE", "BAD-NAME"} {
		err := ValidateRegistry(Registry{SchemaVersion: SchemaVersion, Environments: []Environment{{ID: "dev", Exports: map[string]string{key: "value"}}}})
		if err == nil {
			t.Fatalf("expected %q to be rejected", key)
		}
	}
}

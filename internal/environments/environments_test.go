package environments

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/secrets"
)

type fakeSecretGetter struct {
	values map[string][]byte
	err    error
}

func (getter fakeSecretGetter) Get(service, field string) ([]byte, string, error) {
	if getter.err != nil {
		return nil, "", getter.err
	}
	value, found := getter.values[service+"/"+field]
	if !found {
		return nil, "", &secrets.NotFoundError{Message: "not found"}
	}
	return append([]byte(nil), value...), field, nil
}

type failingWriter struct{ captured []byte }

func (writer *failingWriter) Write(value []byte) (int, error) {
	limit := len(value) / 2
	writer.captured = append(writer.captured, value[:limit]...)
	return limit, errors.New("sentinel write failure")
}

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

func TestSecretReferencesValidateConflictsAndExactSyntax(t *testing.T) {
	valid := Environment{ID: "dev", Exports: map[string]string{}, Secrets: map[string]string{"TOKEN": "sec://service/token"}}
	if err := ValidateRegistry(Registry{SchemaVersion: SchemaVersion, Environments: []Environment{valid}}); err != nil {
		t.Fatalf("valid reference rejected: %v", err)
	}
	for name, candidate := range map[string]Environment{
		"missing field":   {ID: "dev", Secrets: map[string]string{"TOKEN": "sec://service"}},
		"extra segment":   {ID: "dev", Secrets: map[string]string{"TOKEN": "sec://service/token/more"}},
		"invalid service": {ID: "dev", Secrets: map[string]string{"TOKEN": "sec://bad service/token"}},
		"reserved":        {ID: "dev", Secrets: map[string]string{"AWS_PROFILE": "sec://service/token"}},
		"export conflict": {ID: "dev", Exports: map[string]string{"TOKEN": "plain"}, Secrets: map[string]string{"TOKEN": "sec://service/token"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRegistry(Registry{SchemaVersion: SchemaVersion, Environments: []Environment{candidate}}); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestOldRegistryLoadsWithEmptySecretMap(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.EnvironmentsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := "schema_version = 1\n\n[[environments]]\nid = 'dev'\n\n[environments.exports]\nFEATURE = 'on'\n"
	if err := os.WriteFile(paths.EnvironmentsFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := NewStore(paths).List()
	if err != nil || len(items) != 1 || items[0].Secrets == nil || len(items[0].Secrets) != 0 {
		t.Fatalf("legacy load=%#v err=%v", items, err)
	}
}

func TestSecretReferenceHealthAndResolutionAreMetadataOnly(t *testing.T) {
	const sentinel = "SECRET-RESOLUTION-SENTINEL"
	environment := Environment{ID: "dev", Secrets: map[string]string{
		"AVAILABLE":       "sec://service/token",
		"MISSING_FIELD":   "sec://service/missing",
		"MISSING_SERVICE": "sec://other/token",
	}}
	getter := fakeSecretGetter{values: map[string][]byte{"service/token": []byte(sentinel)}}
	statuses := CheckSecretReferences(environment, getter)
	if len(statuses) != 3 || !statuses[0].Available || statuses[1].Reason != "missing" || statuses[2].Reason != "missing" {
		t.Fatalf("statuses=%#v", statuses)
	}
	for _, status := range statuses {
		if strings.Contains(status.Reason, sentinel) {
			t.Fatal("health leaked resolved value")
		}
	}
	resolved, statuses, err := ResolveSecretReferences(environment, getter)
	if err == nil || resolved != nil || len(statuses) != 3 {
		t.Fatalf("resolved=%#v statuses=%#v err=%v", resolved, statuses, err)
	}
	unavailable := CheckSecretReferences(Environment{ID: "dev", Secrets: map[string]string{"TOKEN": "sec://service/token"}}, fakeSecretGetter{err: errors.New("identity raw sentinel")})
	if len(unavailable) != 1 || unavailable[0].Reason != "store_unavailable" || strings.Contains(unavailable[0].Reason, "identity") {
		t.Fatalf("unsanitized unavailable status: %#v", unavailable)
	}
}

func TestWriteShellExportsQuotesSecretsAndReturnsWriteFailure(t *testing.T) {
	const sentinel = "secret's two words"
	environment := Environment{ID: "dev", AWSProfile: "sandbox", Exports: map[string]string{"FEATURE": "on"}, Secrets: map[string]string{"TOKEN": "sec://service/token"}}
	resolved := map[string][]byte{"TOKEN": []byte(sentinel)}
	var output strings.Builder
	if err := WriteShellExports(&output, environment, resolved); err != nil {
		t.Fatal(err)
	}
	expected := "export AWS_PROFILE='sandbox'\nexport FEATURE='on'\nexport TOKEN='secret'\\''s two words'\n"
	if output.String() != expected {
		t.Fatalf("output=%q", output.String())
	}
	failure := &failingWriter{}
	if err := WriteShellExports(failure, environment, resolved); err == nil || !errors.Is(err, io.ErrShortWrite) && !strings.Contains(err.Error(), "sentinel write failure") {
		t.Fatalf("expected write failure, got %v", err)
	}
}

func TestWriteShellExportsRejectsNULBeforeWritingAnything(t *testing.T) {
	for name, testCase := range map[string]struct {
		environment Environment
		resolved    map[string][]byte
	}{
		"ordinary": {environment: Environment{ID: "dev", Exports: map[string]string{"VALUE": "before\x00after"}}},
		"secret": {
			environment: Environment{ID: "dev", Exports: map[string]string{"FIRST": "would otherwise be written"}, Secrets: map[string]string{"TOKEN": "sec://service/token"}},
			resolved:    map[string][]byte{"TOKEN": []byte("secret\x00sentinel")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var output strings.Builder
			err := WriteShellExports(&output, testCase.environment, testCase.resolved)
			if err == nil || output.Len() != 0 {
				t.Fatalf("err=%v output=%q", err, output.String())
			}
			if strings.Contains(err.Error(), "before") || strings.Contains(err.Error(), "after") || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("error leaked value: %v", err)
			}
		})
	}
}

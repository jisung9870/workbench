package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jisung9870/workbench/internal/config"
)

func testPaths(root string) config.Paths {
	configDir := filepath.Join(root, "config", "workbench")
	return config.Paths{
		ConfigDir: configDir, StateDir: filepath.Join(root, "state", "workbench"),
		AgeIdentityFile: filepath.Join(configDir, "age.key"), SecretsFile: filepath.Join(configDir, "secrets.json.age"),
		BackupsDir: filepath.Join(root, "state", "workbench", "backups"),
	}
}

func TestStoreRoundTripModesOverwriteAndCiphertextOnlyBackup(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{paths.ConfigDir, paths.AgeIdentityFile, paths.SecretsFile} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if info.Mode().Perm() != want {
				t.Fatalf("%s mode=%04o want=%04o", path, info.Mode().Perm(), want)
			}
		}
	}
	first := []byte("SENTINEL-first\nsecond line")
	if backup, err := store.Set("svc", "token", first, false); err != nil || backup == "" {
		t.Fatalf("set backup=%q err=%v", backup, err)
	}
	value, field, err := store.Get("svc", "")
	if err != nil || field != "token" || !bytes.Equal(value, first) {
		t.Fatalf("get field=%q value=%q err=%v", field, value, err)
	}
	if _, err := store.Set("svc", "token", []byte("replacement-SENTINEL"), false); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	value, _, _ = store.Get("svc", "token")
	if !bytes.Equal(value, first) {
		t.Fatalf("overwrite changed value: %q", value)
	}
	backups, err := filepath.Glob(filepath.Join(paths.BackupsDir, "secrets.json.age-*"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(paths.BackupsDir)
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("backup directory mode=%04o", info.Mode().Perm())
		}
	}
	for _, backup := range backups {
		data, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("SENTINEL")) {
			t.Fatalf("plaintext leaked into encrypted backup %s", backup)
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(backup)
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("backup mode=%04o", info.Mode().Perm())
			}
		}
	}
	entries, err := store.List("")
	if err != nil || len(entries) != 1 || entries[0] != (Entry{Service: "svc", Field: "token"}) {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if _, err := store.Remove("svc", "token"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get("svc", "token"); err == nil {
		t.Fatal("removed secret still exists")
	}
}

func TestStoreWrongMissingAndMalformedIdentityNeverExposeSentinel(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("svc", "field", []byte("PLAINTEXT-SENTINEL"), false); err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := os.ReadFile(paths.AgeIdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgeIdentityFile, []byte(other.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Get("svc", "field")
	if err == nil || strings.Contains(err.Error(), "PLAINTEXT-SENTINEL") || strings.Contains(err.Error(), other.String()) {
		t.Fatalf("unsafe wrong-key error: %v", err)
	}
	if err := os.WriteFile(paths.AgeIdentityFile, originalIdentity, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.AgeIdentityFile); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Get("svc", "field")
	if err == nil || strings.Contains(err.Error(), "PLAINTEXT-SENTINEL") {
		t.Fatalf("unsafe missing-key error: %v", err)
	}
}

func TestExplicitReplaceCreatesBackupWithPriorCiphertext(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("svc", "token", []byte("OLD-ROTATION-SENTINEL"), false); err != nil {
		t.Fatal(err)
	}
	backup, err := store.Set("svc", "token", []byte("NEW-ROTATION-SENTINEL"), true)
	if err != nil || backup == "" {
		t.Fatalf("replace backup=%q err=%v", backup, err)
	}
	value, _, err := store.Get("svc", "token")
	if err != nil || string(value) != "NEW-ROTATION-SENTINEL" {
		t.Fatalf("new value=%q err=%v", value, err)
	}
	identityBytes, _ := os.ReadFile(paths.AgeIdentityFile)
	identity, err := parseX25519Identity(identityBytes)
	if err != nil {
		t.Fatal(err)
	}
	backupBytes, err := readSecureFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := decrypt(backupBytes, identity)
	if err != nil || prior["svc"]["token"] != "OLD-ROTATION-SENTINEL" {
		t.Fatalf("prior=%v err=%v", prior, err)
	}
}

func TestVerificationFailureRollsBackExactPriorCiphertext(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("svc", "token", []byte("ROLLBACK-OLD-SENTINEL"), false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.SecretsFile)
	if err != nil {
		t.Fatal(err)
	}
	store.verifyCiphertext = func([]byte, *age.X25519Identity) error { return errors.New("injected post-write verification failure") }
	backup, err := store.Set("svc", "other", []byte("ROLLBACK-NEW-SENTINEL"), false)
	if err == nil || backup == "" || !strings.Contains(err.Error(), "prior ciphertext was restored") {
		t.Fatalf("backup=%q err=%v", backup, err)
	}
	after, readErr := os.ReadFile(paths.SecretsFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rollback did not restore the exact prior ciphertext")
	}
	value, _, err := store.Get("svc", "token")
	if err != nil || string(value) != "ROLLBACK-OLD-SENTINEL" {
		t.Fatalf("old value=%q err=%v", value, err)
	}
	if _, _, err := store.Get("svc", "other"); err == nil {
		t.Fatal("failed update survived rollback")
	}
}

func TestCrossProcessMutationsDoNotLoseUpdates(t *testing.T) {
	if os.Getenv("WB_SECRETS_PROCESS_HELPER") == "1" {
		t.Skip("helper is selected by its dedicated test")
	}
	paths := testPaths(t.TempDir())
	if err := NewStore(paths).Init(); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(filepath.Dir(paths.ConfigDir), "start")
	const workers = 12
	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	commands := make([]*child, 0, workers)
	for index := 0; index < workers; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestCrossProcessMutationHelper$", "-test.count=1")
		command.Env = append(os.Environ(),
			"WB_SECRETS_PROCESS_HELPER=1",
			"WB_SECRETS_PROCESS_ROOT="+filepath.Dir(filepath.Dir(paths.ConfigDir)),
			"WB_SECRETS_PROCESS_GATE="+gate,
			"WB_SECRETS_PROCESS_INDEX="+strconv.Itoa(index),
		)
		item := &child{command: command}
		command.Stdout, command.Stderr = &item.output, &item.output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, item)
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, item := range commands {
		if err := item.command.Wait(); err != nil {
			t.Fatalf("child failed: %v %s", err, item.output.String())
		}
	}
	entries, err := NewStore(paths).List("parallel")
	if err != nil || len(entries) != workers {
		t.Fatalf("entries=%d want=%d err=%v", len(entries), workers, err)
	}
}

func TestCrossProcessMutationHelper(t *testing.T) {
	if os.Getenv("WB_SECRETS_PROCESS_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	gate := os.Getenv("WB_SECRETS_PROCESS_GATE")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process gate")
		}
		time.Sleep(5 * time.Millisecond)
	}
	index := os.Getenv("WB_SECRETS_PROCESS_INDEX")
	paths := testPaths(os.Getenv("WB_SECRETS_PROCESS_ROOT"))
	if _, err := NewStore(paths).Set("parallel", "field-"+index, []byte("PROCESS-SENTINEL-"+index), false); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedDecryptedFixtureDoesNotLeakPlaintext(t *testing.T) {
	paths := testPaths(t.TempDir())
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgeIdentityFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	w, err := age.Encrypt(&encrypted, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"svc":{"token":"MALFORMED-SENTINEL"},"bad":42}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SecretsFile, encrypted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = NewStore(paths).Get("svc", "token")
	if err == nil || strings.Contains(err.Error(), "MALFORMED-SENTINEL") {
		t.Fatalf("unsafe malformed error: %v", err)
	}
}

func TestMigrationCheckApplyRetainsLegacyAndRejectsConflict(t *testing.T) {
	root := t.TempDir()
	destination := testPaths(root)
	legacyDir := filepath.Join(root, "legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(legacyDir, "age.key")
	storePath := filepath.Join(legacyDir, "secrets.json.age")
	identityBytes := []byte("# public key: " + identity.Recipient().String() + "\n" + identity.String() + "\n")
	if err := os.WriteFile(identityPath, identityBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := encrypt(identity.Recipient(), Vault{"svc": {"token": "MIGRATION-SENTINEL", "multiline": "one\ntwo"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeIdentity, _ := os.ReadFile(identityPath)
	beforeStore, _ := os.ReadFile(storePath)
	plan, err := PlanMigration(LegacyPaths{Identity: identityPath, Store: storePath}, destination)
	if err != nil || !plan.CanApply || plan.IdentityType != "x25519" || plan.ServiceCount != 1 || plan.FieldCount != 2 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if after, _ := os.ReadFile(storePath); !bytes.Equal(after, beforeStore) {
		t.Fatal("check changed legacy store")
	}
	if err := ApplyMigration(plan, destination); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(identityPath); !bytes.Equal(after, beforeIdentity) {
		t.Fatal("apply changed legacy identity")
	}
	if after, _ := os.ReadFile(storePath); !bytes.Equal(after, beforeStore) {
		t.Fatal("apply changed legacy store")
	}
	value, _, err := NewStore(destination).Get("svc", "token")
	if err != nil || string(value) != "MIGRATION-SENTINEL" {
		t.Fatalf("migrated value=%q err=%v", value, err)
	}
	second, err := PlanMigration(LegacyPaths{Identity: identityPath, Store: storePath}, destination)
	if err != nil || second.CanApply || second.DestinationAvailable {
		t.Fatalf("expected conflict plan=%#v err=%v", second, err)
	}
}

func TestMigrationWrongKeyAndInvalidSchemaAreMetadataOnly(t *testing.T) {
	root := t.TempDir()
	destination := testPaths(root)
	legacyDir := filepath.Join(root, "legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	one, _ := age.GenerateX25519Identity()
	two, _ := age.GenerateX25519Identity()
	identityPath := filepath.Join(legacyDir, "age.key")
	storePath := filepath.Join(legacyDir, "store.age")
	if err := os.WriteFile(identityPath, []byte(two.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := encrypt(one.Recipient(), Vault{"svc": {"token": "WRONG-KEY-SENTINEL"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(LegacyPaths{Identity: identityPath, Store: storePath}, destination)
	encoded, _ := json.Marshal(plan)
	if err != nil || plan.CanApply || plan.DecryptValid || bytes.Contains(encoded, []byte("WRONG-KEY-SENTINEL")) || bytes.Contains(encoded, []byte(two.String())) {
		t.Fatalf("unsafe wrong-key plan=%s err=%v", encoded, err)
	}
}

func TestMigrationReportsSchemaNamesAndModesSeparately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode contract")
	}
	root := t.TempDir()
	destination := testPaths(root)
	legacyDir := filepath.Join(root, "legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, _ := age.GenerateX25519Identity()
	identityPath, storePath := filepath.Join(legacyDir, "key"), filepath.Join(legacyDir, "store")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ciphertext := encryptRawForTest(t, identity.Recipient(), []byte(`{"bad/name":{"field":"NAME-SENTINEL"}}`))
	if err := os.WriteFile(storePath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(LegacyPaths{Identity: identityPath, Store: storePath}, destination)
	if err != nil || !plan.DecryptValid || !plan.SchemaValid || plan.NamesValid || plan.IdentityHealthy || plan.CanApply {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if encoded, _ := json.Marshal(plan); bytes.Contains(encoded, []byte("NAME-SENTINEL")) || bytes.Contains(encoded, []byte(identity.String())) {
		t.Fatalf("migration metadata leaked raw material: %s", encoded)
	}

	if err := os.Chmod(identityPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ciphertext = encryptRawForTest(t, identity.Recipient(), []byte(`{"svc":{"field":42,"token":"SCHEMA-SENTINEL"}}`))
	if err := os.WriteFile(storePath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanMigration(LegacyPaths{Identity: identityPath, Store: storePath}, destination)
	if err != nil || !plan.DecryptValid || plan.SchemaValid || plan.NamesValid || plan.CanApply {
		t.Fatalf("schema plan=%#v err=%v", plan, err)
	}
	if encoded, _ := json.Marshal(plan); bytes.Contains(encoded, []byte("SCHEMA-SENTINEL")) {
		t.Fatalf("schema metadata leaked value: %s", encoded)
	}
}

func TestDuplicateJSONNamesAreRejected(t *testing.T) {
	for _, plaintext := range []string{
		`{"svc":{"field":"one"},"svc":{"field":"two"}}`,
		`{"svc":{"field":"one","field":"two"}}`,
	} {
		if _, err := decodeVault([]byte(plaintext)); err == nil {
			t.Fatalf("accepted duplicate JSON: %s", plaintext)
		}
	}
}

func TestAgeCLIBidirectionalInteroperability(t *testing.T) {
	ageBinary, err := exec.LookPath("age")
	if err != nil {
		t.Skip("age CLI is not installed")
	}
	paths := testPaths(t.TempDir())
	store := NewStore(paths)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("svc", "token", []byte("GO-TO-CLI-SENTINEL"), false); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(ageBinary, "-d", "-i", paths.AgeIdentityFile, paths.SecretsFile)
	plaintext, err := command.Output()
	if err != nil || !bytes.Contains(plaintext, []byte("GO-TO-CLI-SENTINEL")) {
		t.Fatalf("age decrypt=%q err=%v", plaintext, err)
	}

	identityBytes, err := os.ReadFile(paths.AgeIdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := parseX25519Identity(identityBytes)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"cli":{"field":"CLI-TO-GO-SENTINEL"}}`)
	command = exec.Command(ageBinary, "-r", identity.Recipient().String(), "-o", paths.SecretsFile)
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("age encrypt: %v %s", err, output)
	}
	if err := os.Chmod(paths.SecretsFile, 0o600); err != nil {
		t.Fatal(err)
	}
	value, _, err := store.Get("cli", "field")
	if err != nil || string(value) != "CLI-TO-GO-SENTINEL" {
		t.Fatalf("Go decrypt=%q err=%v", value, err)
	}
}

func TestBinboxSecFixtureMigratesWhenRepositoryIsAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("binbox sec is a Bash CLI")
	}
	secPath, err := filepath.Abs(filepath.Join("..", "..", "..", "binbox", "libexec", "sec"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secPath); err != nil {
		t.Skip("sibling binbox checkout is unavailable")
	}
	for _, command := range []string{"age", "age-keygen", "jq", "bash"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skip(command + " is unavailable")
		}
	}
	root := t.TempDir()
	legacy := LegacyPaths{Identity: filepath.Join(root, "binbox", "age.key"), Store: filepath.Join(root, "binbox", "store.age")}
	environment := append(os.Environ(), "BINBOX_AGE_KEY="+legacy.Identity, "BINBOX_SECRETS_FILE="+legacy.Store)
	command := exec.Command(secPath, "init")
	command.Env = environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sec init: %v %s", err, output)
	}
	command = exec.Command(secPath, "set", "legacy", "token")
	command.Env = environment
	command.Stdin = strings.NewReader("BINBOX-INTEROP-SENTINEL")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sec set: %v %s", err, output)
	}
	destination := testPaths(filepath.Join(root, "destination"))
	plan, err := PlanMigration(legacy, destination)
	if err != nil || !plan.CanApply {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if err := ApplyMigration(plan, destination); err != nil {
		t.Fatal(err)
	}
	value, _, err := NewStore(destination).Get("legacy", "token")
	if err != nil || string(value) != "BINBOX-INTEROP-SENTINEL" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}

func TestApplyMigrationRejectsChangedSource(t *testing.T) {
	root := t.TempDir()
	destination := testPaths(root)
	legacyDir := filepath.Join(root, "legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, _ := age.GenerateX25519Identity()
	identityPath, storePath := filepath.Join(legacyDir, "key"), filepath.Join(legacyDir, "store")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ciphertext, _ := encrypt(identity.Recipient(), Vault{})
	if err := os.WriteFile(storePath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(LegacyPaths{Identity: identityPath, Store: storePath}, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, append(ciphertext, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	err = ApplyMigration(plan, destination)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if exists(destination.AgeIdentityFile) || exists(destination.SecretsFile) {
		t.Fatal("changed-source apply created destination")
	}
}

func encryptRawForTest(t *testing.T, recipient age.Recipient, plaintext []byte) []byte {
	t.Helper()
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return ciphertext.Bytes()
}

package compatibility

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreKeepsAllowedTuplesInSeparateBoundedFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "compatibility")
	store := NewStore(directory)
	first := time.Date(2026, 8, 5, 6, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	if err := store.Observe("nvim", "projects", "workbench", first); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe("nvim", "projects", "sessionizer", second); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe("nvim", "projects", "workbench", second); err != nil {
		t.Fatal(err)
	}

	observations, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected two tuple observations, got %#v", observations)
	}
	if observations[0].Source != "sessionizer" || observations[1].Source != "workbench" || !observations[1].LastObservedAt.Equal(second) {
		t.Fatalf("unexpected deterministic observations: %#v", observations)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected one file per observed tuple, got %d", len(entries))
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("unexpected compatibility directory mode: %o", directoryInfo.Mode().Perm())
	}
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("unexpected observation mode for %s: %o", entry.Name(), info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(directory), "backups")); !os.IsNotExist(err) {
		t.Fatalf("compatibility telemetry created backups: %v", err)
	}
}

func TestStoreRejectsUnknownTupleAndTamperedKnownFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "compatibility")
	store := NewStore(directory)
	now := time.Date(2026, 8, 5, 6, 0, 0, 0, time.UTC)
	if err := store.Observe("unknown", "projects", "workbench", now); err == nil {
		t.Fatal("unknown tuple was accepted")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	tampered := `{"schema_version":1,"client":"nvim","feature":"projects","source":"binbox","last_observed_at":"2026-08-05T06:00:00Z","extra":true}`
	if err := os.WriteFile(filepath.Join(directory, "nvim-projects-workbench.json"), []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("tampered known tuple file was accepted")
	}
}

func TestValidateExternalTupleExcludesInternalRegistryObservation(t *testing.T) {
	for _, tuple := range []Observation{
		{Client: "nvim", Feature: "projects", Source: "workbench"},
		{Client: "nvim", Feature: "projects", Source: "binbox"},
		{Client: "nvim", Feature: "projects", Source: "sessionizer"},
		{Client: "binbox", Feature: "agents", Source: "scrape"},
	} {
		if err := ValidateExternal(tuple.Client, tuple.Feature, tuple.Source); err != nil {
			t.Fatalf("external tuple rejected: %#v: %v", tuple, err)
		}
	}
	if err := ValidateExternal("workbench", "agents", "registry"); err == nil {
		t.Fatal("internal registry tuple was exposed through the public CLI")
	}
}

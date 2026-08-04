package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettingsReportsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\nactive_profile = \"personal\"\nunknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSettings(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field \"unknown\"") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadSettingsReportsParserLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\nactive_profile = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSettings(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected parser line, got %v", err)
	}
}

func TestLoadSettingsUsesDefaultsWhenFileIsAbsent(t *testing.T) {
	settings, err := LoadSettings(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if settings.SchemaVersion != SchemaVersion || settings.ActiveProfile != "personal" {
		t.Fatalf("unexpected defaults: %#v", settings)
	}
}

func TestLoadSettingsRejectsProfilePathTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\nactive_profile = \"../outside\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSettings(path)
	if err == nil || !strings.Contains(err.Error(), "invalid active_profile") {
		t.Fatalf("expected profile validation error, got %v", err)
	}
}

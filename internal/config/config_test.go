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

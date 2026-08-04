package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettingsRequiresSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("active_profile = \"personal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSettings(path)
	if err == nil || !strings.Contains(err.Error(), "schema_version 0") {
		t.Fatalf("expected missing schema version error, got %v", err)
	}
}

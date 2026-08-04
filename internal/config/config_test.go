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

func TestLoadProfileUsesWindowsTerminalDefaults(t *testing.T) {
	root := t.TempDir()
	paths := Paths{ProfilesDir: filepath.Join(root, "profiles")}
	if err := os.MkdirAll(paths.ProfilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "personal.toml"), []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadProfile(paths, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if profile.WindowsTerminalWindow != "last" || profile.WindowsTerminalMode != "tab" {
		t.Fatalf("unexpected Windows Terminal defaults: %#v", profile)
	}
}

func TestLoadProfileAcceptsWindowsTerminalPreferences(t *testing.T) {
	root := t.TempDir()
	paths := Paths{ProfilesDir: filepath.Join(root, "profiles")}
	if err := os.MkdirAll(paths.ProfilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version = 1\nwindows_terminal_distro = \"Ubuntu-24.04\"\nwindows_terminal_window = \"_quake\"\nwindows_terminal_mode = \"split-vertical\"\n"
	if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "personal.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadProfile(paths, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if profile.WindowsTerminalDistro != "Ubuntu-24.04" || profile.WindowsTerminalWindow != "_quake" || profile.WindowsTerminalMode != "split-vertical" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestLoadProfileRejectsInvalidWindowsTerminalPreferences(t *testing.T) {
	tests := []string{
		"windows_terminal_window = \"-1\"\n",
		"windows_terminal_mode = \"diagonal\"\n",
		"windows_terminal_distro = \" Debian \"\n",
	}
	for _, extra := range tests {
		t.Run(strings.TrimSpace(extra), func(t *testing.T) {
			root := t.TempDir()
			paths := Paths{ProfilesDir: filepath.Join(root, "profiles")}
			if err := os.MkdirAll(paths.ProfilesDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "personal.toml"), []byte("schema_version = 1\n"+extra), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfile(paths, "personal"); err == nil {
				t.Fatalf("invalid preference was accepted: %s", extra)
			}
		})
	}
}

package config

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePathsUsesXDGDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG path behavior applies to Unix targets")
	}
	environment := map[string]string{
		"XDG_CONFIG_HOME": "/tmp/example-config",
		"XDG_STATE_HOME":  "/tmp/example-state",
	}
	paths, err := resolvePaths("linux", "/home/example", "/unused/config", "/unused/cache", func(key string) string {
		return environment[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigFile != filepath.Join("/tmp/example-config", "workbench", "config.toml") {
		t.Fatalf("unexpected config path: %s", paths.ConfigFile)
	}
	if paths.BackupsDir != filepath.Join("/tmp/example-state", "workbench", "backups") {
		t.Fatalf("unexpected backup path: %s", paths.BackupsDir)
	}
}

func TestResolvePathsUsesWindowsDirectories(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics require a Windows test host")
	}
	environment := map[string]string{
		"APPDATA":      `C:\Users\example\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\example\AppData\Local`,
	}
	paths, err := resolvePaths("windows", `C:\Users\example`, `C:\fallback-config`, `C:\fallback-cache`, func(key string) string {
		return environment[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigDir != filepath.Join(environment["APPDATA"], "workbench") {
		t.Fatalf("unexpected Windows config path: %s", paths.ConfigDir)
	}
	if paths.StateDir != filepath.Join(environment["LOCALAPPDATA"], "workbench") {
		t.Fatalf("unexpected Windows state path: %s", paths.StateDir)
	}
}

func TestResolvePathsRejectsRelativeXDGDirectory(t *testing.T) {
	_, err := resolvePaths("linux", "/home/example", "/unused/config", "/unused/cache", func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return "relative"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected relative XDG_CONFIG_HOME to be rejected")
	}
}

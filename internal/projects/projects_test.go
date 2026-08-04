package projects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/config"
)

func testStore(t *testing.T) (*Store, config.Paths) {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		ConfigDir:    filepath.Join(root, "config", "workbench"),
		StateDir:     filepath.Join(root, "state", "workbench"),
		ConfigFile:   filepath.Join(root, "config", "workbench", "config.toml"),
		ProjectsFile: filepath.Join(root, "config", "workbench", "projects.toml"),
		ProfilesDir:  filepath.Join(root, "config", "workbench", "profiles"),
		BackupsDir:   filepath.Join(root, "state", "workbench", "backups"),
	}
	return NewStore(paths), paths
}

func TestAddRejectsCanonicalDuplicateAndRemovePreservesDirectory(t *testing.T) {
	store, paths := testStore(t)
	projectDir := filepath.Join(t.TempDir(), "Alpha Project")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	project, _, err := store.Add(projectDir, "", "work")
	if err != nil {
		t.Fatal(err)
	}
	if project.ID != "alpha-project" || project.Profile != "work" {
		t.Fatalf("unexpected project: %#v", project)
	}
	if _, _, err := store.Add(filepath.Join(projectDir, "."), "alias", "work"); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("expected canonical duplicate rejection, got %v", err)
	}
	removed, found, backup, err := store.Remove(project.ID)
	if err != nil || !found || removed.Path != projectDir {
		t.Fatalf("unexpected remove result: %#v %t %q %v", removed, found, backup, err)
	}
	if _, err := os.Stat(projectDir); err != nil {
		t.Fatalf("project directory was modified: %v", err)
	}
	if backup == "" {
		t.Fatal("expected registry backup before remove")
	}
	if _, err := os.Stat(paths.ProjectsFile); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsUnknownProjectField(t *testing.T) {
	store, paths := testStore(t)
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version = 1\n\n[[projects]]\nid = \"alpha\"\nname = \"Alpha\"\npath = \"/tmp/alpha\"\nrepo_root = \"/tmp/alpha\"\ndefault_backend = \"auto\"\neditor = \"nvim\"\ntags = []\nprofile = \"personal\"\nextra = true\n"
	if err := os.WriteFile(paths.ProjectsFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "projects.extra") {
		t.Fatalf("expected strict TOML error, got %v", err)
	}
}

func TestDeriveIDFallsBackForNonASCIIName(t *testing.T) {
	if id := DeriveID("프로젝트"); id != "project" {
		t.Fatalf("unexpected fallback id: %s", id)
	}
}

func TestWindowsWSLOverlayRoundTrips(t *testing.T) {
	store, _ := testStore(t)
	projectDir := t.TempDir()
	project := Project{
		ID: "alpha", Name: "Alpha", Path: projectDir, RepoRoot: projectDir,
		DefaultBackend: "windows-terminal", Editor: "nvim", Tags: []string{}, Profile: "personal",
		WindowsWSL: &WindowsWSL{Distro: "Ubuntu-24.04", WSLPath: "/home/me/alpha"},
	}
	if _, err := store.AddMany([]Project{project}); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.Show("alpha")
	if err != nil || !found || loaded.WindowsWSL == nil || loaded.WindowsWSL.WSLPath != "/home/me/alpha" {
		t.Fatalf("overlay did not round-trip: %#v found=%t err=%v", loaded, found, err)
	}
}

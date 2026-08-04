package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

func TestSessionizerCheckAndApply(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent projects")
	direct := filepath.Join(root, "direct project")
	if err := os.MkdirAll(filepath.Join(parent, "alpha project"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(direct, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "dirs")
	if err := os.WriteFile(source, []byte(parent+"\n="+direct+"\n"+filepath.Join(root, "missing")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{
		ConfigDir:    filepath.Join(root, "config", "workbench"),
		ProjectsFile: filepath.Join(root, "config", "workbench", "projects.toml"),
		BackupsDir:   filepath.Join(root, "state", "workbench", "backups"),
	}
	store := projects.NewStore(paths)
	plan, err := PlanSessionizer(source, "personal", store)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Projects) != 2 || len(plan.Warnings) != 1 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if _, err := os.Stat(paths.ProjectsFile); !os.IsNotExist(err) {
		t.Fatal("planning changed the registry")
	}
	backups, err := ApplySessionizer(plan, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected source backup, got %v", backups)
	}
	items, err := store.List()
	if err != nil || len(items) != 2 {
		t.Fatalf("unexpected imported projects: %#v %v", items, err)
	}
}

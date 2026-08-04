package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/jisung9870/workbench/adapters/git"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/projects"
)

type fixture struct {
	root       string
	repository string
	paths      config.Paths
	manager    *Manager
	state      *StateStore
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(root, "project with spaces")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "-c", "user.name=Workbench Test", "-c", "user.email=workbench@example.invalid", "commit", "-m", "initial")
	paths := config.Paths{
		ConfigDir:     filepath.Join(root, "config", "workbench"),
		StateDir:      filepath.Join(root, "state", "workbench"),
		ProjectsFile:  filepath.Join(root, "config", "workbench", "projects.toml"),
		WorktreesFile: filepath.Join(root, "state", "workbench", "worktrees.json"),
		BackupsDir:    filepath.Join(root, "state", "workbench", "backups"),
	}
	projectStore := projects.NewStore(paths)
	if _, _, err := projectStore.Add(repository, "alpha", "personal"); err != nil {
		t.Fatal(err)
	}
	state := NewStateStore(paths)
	manager := NewManager(projectStore, state, gitadapter.New(&backend.OSExecutor{}))
	return fixture{root: root, repository: repository, paths: paths, manager: manager, state: state}
}

func TestCreateListDuplicateDirtyAndCleanRemove(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	created, err := fixture.manager.Create(ctx, "alpha", "feature/test", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !created.Managed || !strings.HasPrefix(created.ID, "wt-alpha-feature-test-") {
		t.Fatalf("unexpected worktree: %#v", created)
	}
	if withinRoot(fixture.repository, created.Path) {
		t.Fatalf("worktree was created inside the main repository: %s", created.Path)
	}
	items, err := fixture.manager.List(ctx, "alpha")
	if err != nil || len(items) != 1 || items[0].ID != created.ID || !items[0].Managed {
		t.Fatalf("unexpected list: %#v err=%v", items, err)
	}
	if _, err := fixture.manager.Create(ctx, "alpha", "feature/test", ""); err == nil {
		t.Fatal("expected duplicate branch rejection")
	} else {
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("expected conflict, got %T: %v", err, err)
		}
	}
	dirtyFile := filepath.Join(created.Path, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.manager.Remove(ctx, created.ID, RemoveOptions{}); err == nil || !strings.Contains(err.Error(), "is dirty") {
		t.Fatalf("expected dirty refusal, got %v", err)
	}
	if _, err := os.Stat(created.Path); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
	if err := os.Remove(dirtyFile); err != nil {
		t.Fatal(err)
	}
	removed, backups, err := fixture.manager.Remove(ctx, created.ID, RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != created.ID || len(backups) == 0 {
		t.Fatalf("unexpected remove result: %#v backups=%v", removed, backups)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists: %v", err)
	}
	if !branchExists(t, fixture.repository, "feature/test") {
		t.Fatal("remove without --delete-branch deleted the branch")
	}
}

func TestDeleteBranchRequiresExactConfirmation(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	created, err := fixture.manager.Create(ctx, "alpha", "feature/delete", "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.manager.Remove(ctx, created.ID, RemoveOptions{DeleteBranch: true, Confirm: func(string) bool { return false }})
	if err == nil || !strings.Contains(err.Error(), "confirmation failed") {
		t.Fatalf("expected confirmation refusal, got %v", err)
	}
	if _, err := os.Stat(created.Path); err != nil {
		t.Fatalf("worktree changed before confirmation: %v", err)
	}
	_, _, err = fixture.manager.Remove(ctx, created.ID, RemoveOptions{DeleteBranch: true, Confirm: func(branch string) bool {
		return branch == "feature/delete"
	}})
	if err != nil {
		t.Fatal(err)
	}
	if branchExists(t, fixture.repository, "feature/delete") {
		t.Fatal("confirmed branch was not deleted")
	}
}

func TestRemoveRejectsRegistryPathThatDoesNotMatchPorcelain(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	created, err := fixture.manager.Create(ctx, "alpha", "feature/drift", "")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := fixture.state.Load()
	if err != nil {
		t.Fatal(err)
	}
	fakePath := filepath.Join(defaultRoot(fixture.repository, "alpha"), "fake-target")
	if err := os.Mkdir(fakePath, 0o700); err != nil {
		t.Fatal(err)
	}
	registry.Worktrees[0].Path = fakePath
	if _, err := fixture.state.save(registry); err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.manager.Remove(ctx, created.ID, RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "target no longer matches git porcelain") {
		t.Fatalf("expected target mismatch refusal, got %v", err)
	}
	if _, err := os.Stat(created.Path); err != nil {
		t.Fatalf("actual worktree was changed: %v", err)
	}
}

func TestExternalWorktreeIsListedButCannotBeRemoved(t *testing.T) {
	fixture := newFixture(t)
	externalPath := filepath.Join(fixture.root, "external")
	runGit(t, fixture.repository, "worktree", "add", "-b", "feature/external", externalPath, "HEAD")
	items, err := fixture.manager.List(context.Background(), "alpha")
	if err != nil || len(items) != 1 || items[0].Managed {
		t.Fatalf("unexpected external list: %#v err=%v", items, err)
	}
	_, _, err = fixture.manager.Remove(context.Background(), items[0].ID, RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("expected unmanaged refusal, got %v", err)
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func branchExists(t *testing.T, repository, branch string) bool {
	t.Helper()
	command := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	command.Dir = repository
	err := command.Run()
	if err == nil {
		return true
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git show-ref failed: %v", err)
	return false
}

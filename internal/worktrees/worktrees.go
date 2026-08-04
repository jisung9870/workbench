package worktrees

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	gitadapter "github.com/jisung9870/workbench/adapters/git"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type GitClient interface {
	TopLevel(context.Context, string) (string, error)
	ListWorktrees(context.Context, string) ([]gitadapter.Worktree, error)
	ValidateBranch(context.Context, string, string) error
	BranchExists(context.Context, string, string) (bool, error)
	ValidateBase(context.Context, string, string) error
	AddWorktree(context.Context, string, string, string, string, bool) (backend.ProcessResult, error)
	Dirty(context.Context, string) (bool, error)
	RemoveWorktree(context.Context, string, string) (backend.ProcessResult, error)
	DeleteBranch(context.Context, string, string) (backend.ProcessResult, error)
}

type ProjectStore interface {
	Show(string) (projects.Project, bool, error)
}

type Item struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Branch      string `json:"branch,omitempty"`
	Path        string `json:"path"`
	RepoRoot    string `json:"repo_root"`
	Head        string `json:"head"`
	Dirty       bool   `json:"dirty"`
	Managed     bool   `json:"managed"`
	Detached    bool   `json:"detached"`
	Locked      bool   `json:"locked"`
	LockReason  string `json:"lock_reason,omitempty"`
	Prunable    bool   `json:"prunable"`
	PruneReason string `json:"prune_reason,omitempty"`
	Drifted     bool   `json:"drifted"`
}

type ConflictError struct{ Message string }

func (err *ConflictError) Error() string { return err.Message }

type InvalidError struct {
	Message string
	Cause   error
}

func (err *InvalidError) Error() string { return fmt.Sprintf("%s: %v", err.Message, err.Cause) }
func (err *InvalidError) Unwrap() error { return err.Cause }

type PartialError struct {
	Message string
	Item    Item
	Backups []string
	Cause   error
}

func (err *PartialError) Error() string { return fmt.Sprintf("%s: %v", err.Message, err.Cause) }
func (err *PartialError) Unwrap() error { return err.Cause }

type Manager struct {
	projects ProjectStore
	state    *StateStore
	git      GitClient
	now      func() time.Time
}

func NewManager(projectStore ProjectStore, state *StateStore, git GitClient) *Manager {
	return &Manager{projects: projectStore, state: state, git: git, now: time.Now}
}

func (manager *Manager) List(ctx context.Context, projectID string) ([]Item, error) {
	project, repository, err := manager.project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	actual, err := manager.git.ListWorktrees(ctx, repository)
	if err != nil {
		return nil, err
	}
	registry, err := manager.state.Load()
	if err != nil {
		return nil, err
	}
	records := map[string]Record{}
	for _, record := range registry.Worktrees {
		if record.ProjectID == project.ID {
			records[cleanKey(record.Path)] = record
		}
	}
	items := []Item{}
	for _, worktree := range actual {
		path, canonicalErr := projects.CanonicalPath(worktree.Path)
		if canonicalErr != nil {
			if !worktree.Prunable {
				return nil, canonicalErr
			}
			path = filepath.Clean(worktree.Path)
		}
		if samePath(path, repository) {
			continue
		}
		dirty := false
		if !worktree.Prunable {
			var dirtyErr error
			dirty, dirtyErr = manager.git.Dirty(ctx, path)
			if dirtyErr != nil {
				return nil, dirtyErr
			}
		}
		record, managed := records[cleanKey(path)]
		id := stableID(project.ID, worktree.Branch, worktree.Head)
		drifted := false
		if managed {
			id = record.ID
			drifted = record.Branch != worktree.Branch || !samePath(record.RepoRoot, repository)
		}
		items = append(items, Item{
			ID: id, ProjectID: project.ID, Branch: worktree.Branch, Path: path, RepoRoot: repository,
			Head: worktree.Head, Dirty: dirty, Managed: managed, Detached: worktree.Detached,
			Locked: worktree.Locked, LockReason: worktree.LockReason, Prunable: worktree.Prunable,
			PruneReason: worktree.PruneReason, Drifted: drifted,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (manager *Manager) Create(ctx context.Context, projectID, branch, base string) (Item, error) {
	project, repository, err := manager.project(ctx, projectID)
	if err != nil {
		return Item{}, err
	}
	if err := manager.git.ValidateBranch(ctx, repository, branch); err != nil {
		return Item{}, &InvalidError{Message: fmt.Sprintf("invalid branch %q", branch), Cause: err}
	}
	actual, err := manager.git.ListWorktrees(ctx, repository)
	if err != nil {
		return Item{}, err
	}
	for _, worktree := range actual {
		if worktree.Branch == branch {
			return Item{}, &ConflictError{Message: fmt.Sprintf("branch %q is already checked out at %s", branch, worktree.Path)}
		}
	}
	branchExists, err := manager.git.BranchExists(ctx, repository, branch)
	if err != nil {
		return Item{}, err
	}
	if branchExists && base != "" {
		return Item{}, &ConflictError{Message: fmt.Sprintf("branch %q already exists; --base is only valid for a new branch", branch)}
	}
	if !branchExists {
		if base == "" {
			base = "HEAD"
		}
		if err := manager.git.ValidateBase(ctx, repository, base); err != nil {
			return Item{}, &InvalidError{Message: fmt.Sprintf("invalid base %q", base), Cause: err}
		}
	}
	id := stableID(project.ID, branch, "")
	registry, err := manager.state.Load()
	if err != nil {
		return Item{}, err
	}
	for _, record := range registry.Worktrees {
		if record.ID == id {
			return Item{}, &ConflictError{Message: fmt.Sprintf("stable worktree id %q is already registered for %s", id, record.Path)}
		}
	}
	destination := filepath.Join(defaultRoot(repository, project.ID), id)
	if _, err := os.Lstat(destination); err == nil {
		return Item{}, &ConflictError{Message: fmt.Sprintf("worktree destination already exists: %s", destination)}
	} else if !os.IsNotExist(err) {
		return Item{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Item{}, fmt.Errorf("create worktree parent directory: %w", err)
	}
	if _, err := manager.git.AddWorktree(ctx, repository, destination, branch, base, branchExists); err != nil {
		return Item{}, err
	}
	verified, err := manager.findActual(ctx, repository, destination)
	if err != nil {
		return Item{}, &PartialError{Message: "worktree was created but verification failed", Item: Item{ID: id, ProjectID: project.ID, Branch: branch, Path: destination, RepoRoot: repository}, Cause: err}
	}
	if verified.Branch != branch {
		return Item{}, &PartialError{Message: "worktree was created with an unexpected branch", Item: Item{ID: id, ProjectID: project.ID, Branch: verified.Branch, Path: destination, RepoRoot: repository}, Cause: fmt.Errorf("expected branch %q", branch)}
	}
	record := Record{ID: id, ProjectID: project.ID, Branch: branch, Path: destination, RepoRoot: repository, CreatedAt: manager.now().UTC()}
	backup, err := manager.state.Add(record)
	item := itemFrom(record, verified, false)
	if err != nil {
		return item, &PartialError{Message: "worktree was created but registry update failed", Item: item, Backups: nonEmpty(backup), Cause: err}
	}
	item.Managed = true
	return item, nil
}

type RemoveOptions struct {
	DeleteBranch bool
	Confirm      func(branch string) bool
}

func (manager *Manager) Remove(ctx context.Context, id string, options RemoveOptions) (Item, []string, error) {
	registry, err := manager.state.Load()
	if err != nil {
		return Item{}, nil, err
	}
	var record Record
	found := false
	for _, candidate := range registry.Worktrees {
		if candidate.ID == id {
			record = candidate
			found = true
			break
		}
	}
	if !found {
		return Item{}, nil, &ConflictError{Message: fmt.Sprintf("worktree %q is not managed by Workbench", id)}
	}
	_, repository, err := manager.project(ctx, record.ProjectID)
	if err != nil {
		return Item{}, nil, err
	}
	if !samePath(record.RepoRoot, repository) {
		return Item{}, nil, &ConflictError{Message: fmt.Sprintf("worktree %q repository root drifted from %s to %s", id, record.RepoRoot, repository)}
	}
	target, err := projects.CanonicalPath(record.Path)
	if err != nil {
		return Item{}, nil, &ConflictError{Message: fmt.Sprintf("worktree %q registered path is unavailable: %v", id, err)}
	}
	managedRoot, err := projects.CanonicalPath(defaultRoot(repository, record.ProjectID))
	if err != nil {
		return Item{}, nil, &ConflictError{Message: fmt.Sprintf("worktree %q managed root is unavailable: %v", id, err)}
	}
	if !samePath(target, record.Path) || !withinRoot(managedRoot, target) {
		return Item{}, nil, &ConflictError{Message: fmt.Sprintf("worktree %q path is outside the managed root: %s", id, record.Path)}
	}
	actual, err := manager.findActual(ctx, repository, target)
	if err != nil {
		return Item{}, nil, &ConflictError{Message: fmt.Sprintf("worktree %q target no longer matches git porcelain: %v", id, err)}
	}
	item := itemFrom(record, actual, true)
	if actual.Branch != record.Branch {
		return item, nil, &ConflictError{Message: fmt.Sprintf("worktree %q branch drifted from %q to %q", id, record.Branch, actual.Branch)}
	}
	if actual.Locked {
		return item, nil, &ConflictError{Message: fmt.Sprintf("worktree %q is locked: %s", id, actual.LockReason)}
	}
	dirty, err := manager.git.Dirty(ctx, target)
	if err != nil {
		return item, nil, err
	}
	item.Dirty = dirty
	if dirty {
		return item, nil, &ConflictError{Message: fmt.Sprintf("worktree %q is dirty; commit, stash, or clean it before removal", id)}
	}
	if options.DeleteBranch {
		if options.Confirm == nil || !options.Confirm(record.Branch) {
			return item, nil, &ConflictError{Message: fmt.Sprintf("branch deletion confirmation failed for %q", record.Branch)}
		}
	}
	if _, err := manager.git.RemoveWorktree(ctx, repository, target); err != nil {
		return item, nil, err
	}
	stillPresent, verifyErr := manager.containsActual(ctx, repository, target)
	if verifyErr != nil {
		return item, nil, &PartialError{Message: "worktree was removed but verification failed", Item: item, Cause: verifyErr}
	}
	if stillPresent {
		return item, nil, &PartialError{Message: "git reported success but the worktree is still present", Item: item, Cause: fmt.Errorf("porcelain still contains %s", record.Path)}
	}
	backups := []string{}
	backup, err := manager.state.Remove(id)
	if backup != "" {
		backups = append(backups, backup)
	}
	if err != nil {
		return item, backups, &PartialError{Message: "worktree was removed but registry update failed", Item: item, Backups: backups, Cause: err}
	}
	if options.DeleteBranch {
		if _, err := manager.git.DeleteBranch(ctx, repository, record.Branch); err != nil {
			return item, backups, &PartialError{Message: "worktree was removed but branch deletion failed", Item: item, Backups: backups, Cause: err}
		}
	}
	return item, backups, nil
}

func (manager *Manager) containsActual(ctx context.Context, repository, target string) (bool, error) {
	actual, err := manager.git.ListWorktrees(ctx, repository)
	if err != nil {
		return false, err
	}
	for _, worktree := range actual {
		if samePath(worktree.Path, target) {
			return true, nil
		}
	}
	return false, nil
}

func (manager *Manager) project(ctx context.Context, id string) (projects.Project, string, error) {
	project, found, err := manager.projects.Show(id)
	if err != nil {
		return projects.Project{}, "", err
	}
	if !found {
		return projects.Project{}, "", fmt.Errorf("project %q was not found", id)
	}
	repository, err := projects.CanonicalPath(project.RepoRoot)
	if err != nil {
		return projects.Project{}, "", err
	}
	topLevel, err := manager.git.TopLevel(ctx, repository)
	if err != nil {
		return projects.Project{}, "", err
	}
	if !samePath(repository, topLevel) {
		return projects.Project{}, "", &ConflictError{Message: fmt.Sprintf("project %q repo_root %s does not match git top-level %s", id, repository, topLevel)}
	}
	return project, repository, nil
}

func (manager *Manager) findActual(ctx context.Context, repository, target string) (gitadapter.Worktree, error) {
	actual, err := manager.git.ListWorktrees(ctx, repository)
	if err != nil {
		return gitadapter.Worktree{}, err
	}
	for _, worktree := range actual {
		if samePath(worktree.Path, target) {
			return worktree, nil
		}
	}
	return gitadapter.Worktree{}, fmt.Errorf("path %s is absent from git worktree list --porcelain", target)
}

func itemFrom(record Record, actual gitadapter.Worktree, managed bool) Item {
	return Item{
		ID: record.ID, ProjectID: record.ProjectID, Branch: actual.Branch, Path: record.Path,
		RepoRoot: record.RepoRoot, Head: actual.Head, Managed: managed, Detached: actual.Detached,
		Locked: actual.Locked, LockReason: actual.LockReason, Prunable: actual.Prunable, PruneReason: actual.PruneReason,
	}
}

func stableID(projectID, branch, head string) string {
	identity := branch
	if identity == "" {
		identity = "detached-" + short(head, 12)
	}
	slug := projects.DeriveID(identity)
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-._")
	}
	hash := sha256.Sum256([]byte(projectID + "\x00" + identity))
	return fmt.Sprintf("wt-%s-%s-%x", projectID, slug, hash[:4])
}

func defaultRoot(repository, projectID string) string {
	return filepath.Join(filepath.Dir(repository), ".worktrees", projectID)
}

func withinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		path = strings.ToLower(path)
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func cleanKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func samePath(left, right string) bool { return cleanKey(left) == cleanKey(right) }

func short(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length]
}

func nonEmpty(value string) []string {
	if value == "" {
		return []string{}
	}
	return []string{value}
}

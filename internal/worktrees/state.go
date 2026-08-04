package worktrees

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

const SchemaVersion = 1

type Record struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Branch    string    `json:"branch"`
	Path      string    `json:"path"`
	RepoRoot  string    `json:"repo_root"`
	CreatedAt time.Time `json:"created_at"`
}

type Registry struct {
	SchemaVersion int      `json:"schema_version"`
	Worktrees     []Record `json:"worktrees"`
}

type StateStore struct {
	path       string
	backupsDir string
}

func NewStateStore(paths config.Paths) *StateStore {
	path := paths.WorktreesFile
	if path == "" {
		path = filepath.Join(paths.StateDir, "worktrees.json")
	}
	return &StateStore{path: path, backupsDir: paths.BackupsDir}
}

func (store *StateStore) Load() (Registry, error) {
	file, err := os.Open(store.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{SchemaVersion: SchemaVersion, Worktrees: []Record{}}, nil
		}
		return Registry{}, fmt.Errorf("open worktree registry: %w", err)
	}
	defer file.Close()
	registry := Registry{}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, fmt.Errorf("%s: %w", store.path, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Registry{}, fmt.Errorf("%s: %w", store.path, err)
	}
	if registry.Worktrees == nil {
		registry.Worktrees = []Record{}
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, fmt.Errorf("%s: %w", store.path, err)
	}
	sort.Slice(registry.Worktrees, func(i, j int) bool { return registry.Worktrees[i].ID < registry.Worktrees[j].ID })
	return registry, nil
}

func (store *StateStore) Add(record Record) (string, error) {
	registry, err := store.Load()
	if err != nil {
		return "", err
	}
	registry.Worktrees = append(registry.Worktrees, record)
	return store.save(registry)
}

func (store *StateStore) Remove(id string) (string, error) {
	registry, err := store.Load()
	if err != nil {
		return "", err
	}
	for index, record := range registry.Worktrees {
		if record.ID == id {
			registry.Worktrees = append(registry.Worktrees[:index], registry.Worktrees[index+1:]...)
			return store.save(registry)
		}
	}
	return "", fmt.Errorf("worktree %q is not registered", id)
}

func (store *StateStore) save(registry Registry) (string, error) {
	if err := validateRegistry(registry); err != nil {
		return "", err
	}
	sort.Slice(registry.Worktrees, func(i, j int) bool { return registry.Worktrees[i].ID < registry.Worktrees[j].ID })
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode worktree registry: %w", err)
	}
	data = append(data, '\n')
	backup, err := storage.Backup(store.path, store.backupsDir, "worktrees.json")
	if err != nil {
		return "", err
	}
	if err := storage.WriteAtomic(store.path, data); err != nil {
		return backup, err
	}
	if _, err := store.Load(); err != nil {
		return backup, fmt.Errorf("validate written worktree registry: %w", err)
	}
	return backup, nil
}

func validateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (expected %d)", registry.SchemaVersion, SchemaVersion)
	}
	ids := map[string]struct{}{}
	paths := map[string]string{}
	for index, record := range registry.Worktrees {
		if record.ID == "" || record.ProjectID == "" || record.Branch == "" || record.Path == "" || record.RepoRoot == "" {
			return fmt.Errorf("worktrees[%d] has an empty required field", index)
		}
		if _, exists := ids[record.ID]; exists {
			return fmt.Errorf("duplicate worktree id %q", record.ID)
		}
		ids[record.ID] = struct{}{}
		if !filepath.IsAbs(record.Path) || !filepath.IsAbs(record.RepoRoot) {
			return fmt.Errorf("worktrees[%d] paths must be absolute", index)
		}
		cleanPath := filepath.Clean(record.Path)
		if runtime.GOOS == "windows" {
			cleanPath = strings.ToLower(cleanPath)
		}
		if owner, exists := paths[cleanPath]; exists {
			return fmt.Errorf("worktree path %q is already owned by %q", record.Path, owner)
		}
		paths[cleanPath] = record.ID
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values are not allowed")
}

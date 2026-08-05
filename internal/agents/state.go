package agents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

const SchemaVersion = 1

type State string

const (
	Starting  State = "starting"
	Running   State = "running"
	Waiting   State = "waiting"
	Idle      State = "idle"
	Completed State = "completed"
	Failed    State = "failed"
	Stopped   State = "stopped"
)

const (
	SourceRegistry = "registry"
	SourceScrape   = "scrape"
)

type Task struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id"`
	WorktreeID     string            `json:"worktree_id,omitempty"`
	AgentKind      string            `json:"agent_kind"`
	Backend        backend.Name      `json:"backend"`
	BackendRef     string            `json:"backend_ref,omitempty"`
	BackendDetails map[string]string `json:"backend_details,omitempty"`
	State          State             `json:"state"`
	StateSource    string            `json:"state_source"`
	RequestSummary string            `json:"request_summary,omitempty"`
	Command        []string          `json:"command,omitempty"`
	CWD            string            `json:"cwd"`
	PID            int               `json:"pid,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	LastEventAt    time.Time         `json:"last_event_at"`
	CompletedAt    *time.Time        `json:"completed_at,omitempty"`
	ExitCode       *int              `json:"exit_code,omitempty"`
}

type Registry struct {
	SchemaVersion int    `json:"schema_version"`
	Tasks         []Task `json:"tasks"`
}

var stateStoreLocks sync.Map

type StateStore struct {
	path       string
	backupsDir string
	mu         *sync.Mutex
}

func NewStateStore(paths config.Paths) *StateStore {
	path := paths.AgentsFile
	if path == "" {
		path = filepath.Join(paths.StateDir, "agents.json")
	}
	lock, _ := stateStoreLocks.LoadOrStore(path, &sync.Mutex{})
	return &StateStore{path: path, backupsDir: paths.BackupsDir, mu: lock.(*sync.Mutex)}
}

func (store *StateStore) Load() (Registry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.load()
}

func (store *StateStore) List(projectID string) ([]Task, error) {
	registry, err := store.Load()
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(registry.Tasks))
	for _, task := range registry.Tasks {
		if projectID == "" || task.ProjectID == projectID {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (store *StateStore) Show(id string) (Task, bool, error) {
	registry, err := store.Load()
	if err != nil {
		return Task{}, false, err
	}
	for _, task := range registry.Tasks {
		if task.ID == id {
			return task, true, nil
		}
	}
	return Task{}, false, nil
}

func (store *StateStore) Create(task Task) (backup string, err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.acquireRegistryLock()
	if err != nil {
		return "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	registry, err := store.load()
	if err != nil {
		return "", err
	}
	for _, existing := range registry.Tasks {
		if existing.ID == task.ID {
			return "", fmt.Errorf("agent task %q is already registered", task.ID)
		}
	}
	registry.Tasks = append(registry.Tasks, task)
	return store.save(registry)
}

func (store *StateStore) Update(id string, update func(*Task) error) (updated Task, backup string, err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.acquireRegistryLock()
	if err != nil {
		return Task{}, "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	registry, err := store.load()
	if err != nil {
		return Task{}, "", err
	}
	for index := range registry.Tasks {
		if registry.Tasks[index].ID != id {
			continue
		}
		before := registry.Tasks[index].State
		candidate := registry.Tasks[index]
		if err := update(&candidate); err != nil {
			return Task{}, "", err
		}
		if candidate.State != before && !validTransition(before, candidate.State) {
			return Task{}, "", fmt.Errorf("invalid agent task transition %s -> %s", before, candidate.State)
		}
		registry.Tasks[index] = candidate
		backup, err = store.save(registry)
		return candidate, backup, err
	}
	return Task{}, "", fmt.Errorf("agent task %q is not registered", id)
}

func (store *StateStore) PruneTerminal(projectID string, taskIDs []string) (removed int, backup string, err error) {
	if projectID == "" || len(taskIDs) == 0 {
		return 0, "", errors.New("project ID and terminal task IDs are required to prune agent history")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.acquireRegistryLock()
	if err != nil {
		return 0, "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	registry, err := store.load()
	if err != nil {
		return 0, "", err
	}
	requested := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		if id == "" {
			return 0, "", errors.New("terminal task IDs cannot be empty")
		}
		if _, duplicate := requested[id]; duplicate {
			return 0, "", fmt.Errorf("duplicate terminal task ID %q", id)
		}
		requested[id] = struct{}{}
	}
	matched := make(map[string]struct{}, len(taskIDs))
	for _, task := range registry.Tasks {
		if _, exists := requested[task.ID]; !exists {
			continue
		}
		if task.ProjectID != projectID || !IsTerminalState(task.State) {
			return 0, "", &ConflictError{Message: fmt.Sprintf("agent task %q is no longer terminal history for project %q", task.ID, projectID)}
		}
		matched[task.ID] = struct{}{}
	}
	if len(matched) != len(requested) {
		return 0, "", &ConflictError{Message: "agent history changed; refresh and confirm the current records"}
	}
	kept := make([]Task, 0, len(registry.Tasks))
	removed = 0
	for _, task := range registry.Tasks {
		if _, remove := requested[task.ID]; remove {
			removed++
			continue
		}
		kept = append(kept, task)
	}
	registry.Tasks = kept
	backup, err = store.save(registry)
	return removed, backup, err
}

func (store *StateStore) acquireRegistryLock() (func() error, error) {
	return acquireRegistryFileLock(store.path + ".lock")
}

func NewTaskID(now time.Time) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return fmt.Sprintf("task-%x-%s", now.UTC().UnixMilli(), hex.EncodeToString(random)), nil
}

func (store *StateStore) load() (Registry, error) {
	file, err := os.Open(store.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{SchemaVersion: SchemaVersion, Tasks: []Task{}}, nil
		}
		return Registry{}, fmt.Errorf("open agent registry: %w", err)
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
	if registry.Tasks == nil {
		registry.Tasks = []Task{}
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, fmt.Errorf("%s: %w", store.path, err)
	}
	sortTasks(registry.Tasks)
	return registry, nil
}

func (store *StateStore) save(registry Registry) (string, error) {
	if err := validateRegistry(registry); err != nil {
		return "", err
	}
	sortTasks(registry.Tasks)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode agent registry: %w", err)
	}
	data = append(data, '\n')
	backup, err := storage.Backup(store.path, store.backupsDir, "agents.json")
	if err != nil {
		return "", err
	}
	if err := storage.WriteAtomic(store.path, data); err != nil {
		return backup, err
	}
	return backup, nil
}

func validateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (expected %d)", registry.SchemaVersion, SchemaVersion)
	}
	ids := map[string]struct{}{}
	for index, task := range registry.Tasks {
		if task.ID == "" || task.ProjectID == "" || task.AgentKind == "" || task.Backend == "" || task.State == "" || task.StateSource == "" || task.CWD == "" {
			return fmt.Errorf("tasks[%d] has an empty required field", index)
		}
		if _, exists := ids[task.ID]; exists {
			return fmt.Errorf("duplicate agent task id %q", task.ID)
		}
		ids[task.ID] = struct{}{}
		if task.AgentKind != "codex" && task.AgentKind != "claude" {
			return fmt.Errorf("tasks[%d] has unsupported agent_kind %q", index, task.AgentKind)
		}
		if !validState(task.State) {
			return fmt.Errorf("tasks[%d] has invalid state %q", index, task.State)
		}
		if task.StateSource == SourceScrape {
			if !strings.HasPrefix(task.ID, "legacy:") {
				return fmt.Errorf("tasks[%d] scrape source requires a legacy: id", index)
			}
		} else if task.StateSource != SourceRegistry {
			return fmt.Errorf("tasks[%d] has invalid state_source %q", index, task.StateSource)
		}
		if !filepath.IsAbs(task.CWD) {
			return fmt.Errorf("tasks[%d] cwd must be absolute", index)
		}
		if task.StartedAt.IsZero() || task.LastEventAt.IsZero() {
			return fmt.Errorf("tasks[%d] timestamps are required", index)
		}
		if task.State != Starting && task.State != Failed && task.BackendRef == "" {
			return fmt.Errorf("tasks[%d] state %q requires backend_ref", index, task.State)
		}
	}
	return nil
}

func validState(state State) bool {
	switch state {
	case Starting, Running, Waiting, Idle, Completed, Failed, Stopped:
		return true
	default:
		return false
	}
}

func IsActiveState(state State) bool {
	return state == Starting || state == Running || state == Waiting || state == Idle
}

func IsTerminalState(state State) bool {
	return state == Completed || state == Failed || state == Stopped
}

func validTransition(from, to State) bool {
	if from == to {
		return true
	}
	switch from {
	case Starting:
		return to == Running || to == Failed || to == Stopped
	case Running, Waiting, Idle:
		return to == Running || to == Waiting || to == Idle || to == Completed || to == Failed || to == Stopped
	default:
		return false
	}
}

func sortTasks(tasks []Task) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].StartedAt.Equal(tasks[j].StartedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
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

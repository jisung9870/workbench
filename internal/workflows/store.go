package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

const historyLimit = 50
const backupLimit = 5

type registry struct {
	SchemaVersion int      `json:"schema_version"`
	Results       []Result `json:"results"`
}

var storeLocks sync.Map

type Store struct {
	path, backups string
	mu            *sync.Mutex
}

func NewStore(paths config.Paths) *Store {
	path := paths.WorkflowsFile
	if path == "" {
		path = filepath.Join(paths.StateDir, "workflows.json")
	}
	lock, _ := storeLocks.LoadOrStore(path, &sync.Mutex{})
	return &Store{path: path, backups: paths.BackupsDir, mu: lock.(*sync.Mutex)}
}

func (s *Store) List(projectID string) ([]Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.load()
	if err != nil {
		return nil, err
	}
	items := []Result{}
	for _, item := range r.Results {
		if projectID == "" || item.ProjectID == projectID {
			items = append(items, item)
		}
	}
	return items, nil
}
func (s *Store) Show(id string) (Result, bool, error) {
	items, err := s.List("")
	if err != nil {
		return Result{}, false, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return Result{}, false, nil
}
func (s *Store) Append(result Result) (backup string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireHistoryFileLock(s.path + ".lock")
	if err != nil {
		return "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	r, err := s.load()
	if err != nil {
		return "", err
	}
	if err := validateResult(result); err != nil {
		return "", err
	}
	r.Results = append([]Result{result}, r.Results...)
	if len(r.Results) > historyLimit {
		r.Results = r.Results[:historyLimit]
	}
	return s.save(r)
}

func (s *Store) Create(result Result) (string, error) {
	return s.mutate(func(r *registry) error {
		for _, item := range r.Results {
			if item.ID == result.ID {
				return &ConflictError{Message: "workflow run already exists"}
			}
		}
		r.Results = append([]Result{result}, r.Results...)
		return nil
	})
}
func (s *Store) Claim(id string) (Result, string, error) {
	var claimed Result
	backup, err := s.mutate(func(r *registry) error {
		for i := range r.Results {
			if r.Results[i].ID != id {
				continue
			}
			if r.Results[i].Status != Pending {
				return &ConflictError{Message: fmt.Sprintf("workflow run %s cannot be claimed from %s", id, r.Results[i].Status)}
			}
			r.Results[i].Status = Running
			claimed = r.Results[i]
			return nil
		}
		return &ConflictError{Message: "workflow run not found"}
	})
	return claimed, backup, err
}
func (s *Store) MarkLaunched(id string, location LaunchLocation) (Result, string, error) {
	var updated Result
	backup, err := s.mutate(func(r *registry) error {
		for i := range r.Results {
			if r.Results[i].ID != id {
				continue
			}
			if r.Results[i].Status == Pending {
				r.Results[i].Status = Running
			}
			r.Results[i].PaneID, r.Results[i].SessionName = location.PaneID, location.SessionName
			updated = r.Results[i]
			return nil
		}
		return &ConflictError{Message: "workflow run not found"}
	})
	return updated, backup, err
}
func (s *Store) FailLaunch(id, message string) (Result, string, error) {
	code := -1
	return s.completeActive(id, Result{Status: Failed, ExitCode: &code, FinishedAt: time.Now().UTC()})
}
func (s *Store) Complete(id string, completion Result) (Result, string, error) {
	return s.completeActive(id, completion)
}
func (s *Store) completeActive(id string, completion Result) (Result, string, error) {
	var updated Result
	backup, err := s.mutate(func(r *registry) error {
		for i := range r.Results {
			if r.Results[i].ID != id {
				continue
			}
			if r.Results[i].Status != Pending && r.Results[i].Status != Running {
				return &ConflictError{Message: fmt.Sprintf("workflow run %s is already %s", id, r.Results[i].Status)}
			}
			current := r.Results[i]
			current.Status, current.ExitCode, current.FinishedAt, current.DurationMillis, current.Output, current.OutputTruncated = completion.Status, completion.ExitCode, completion.FinishedAt, completion.DurationMillis, "", completion.OutputTruncated
			if current.DurationMillis == 0 {
				current.DurationMillis = current.FinishedAt.Sub(current.StartedAt).Milliseconds()
			}
			r.Results[i], updated = current, current
			return nil
		}
		return &ConflictError{Message: "workflow run not found"}
	})
	return updated, backup, err
}
func (s *Store) mutate(change func(*registry) error) (backup string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireHistoryFileLock(s.path + ".lock")
	if err != nil {
		return "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	r, err := s.load()
	if err != nil {
		return "", err
	}
	if err := change(&r); err != nil {
		return "", err
	}
	if len(r.Results) > historyLimit {
		r.Results = r.Results[:historyLimit]
	}
	return s.save(r)
}

func (s *Store) load() (registry, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return registry{SchemaVersion: 1, Results: []Result{}}, nil
	}
	if err != nil {
		return registry{}, err
	}
	defer file.Close()
	var r registry
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return registry{}, fmt.Errorf("decode workflow history: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return registry{}, errors.New("workflow history contains multiple JSON values")
		}
		return registry{}, err
	}
	if r.SchemaVersion != 1 {
		return registry{}, fmt.Errorf("unsupported workflow history schema_version %d", r.SchemaVersion)
	}
	if r.Results == nil {
		r.Results = []Result{}
	}
	for _, item := range r.Results {
		if err := validateResult(item); err != nil {
			return registry{}, fmt.Errorf("invalid workflow history: %w", err)
		}
	}
	return r, nil
}

func validateResult(result Result) error {
	if result.ID == "" || result.WorkflowID == "" || result.ProjectID == "" || result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		return errors.New("workflow result has an empty required field")
	}
	switch result.Status {
	case Succeeded, Failed, TimedOut, Cancelled, Pending, Running:
	default:
		return fmt.Errorf("workflow result has invalid status %q", result.Status)
	}
	if result.FinishedAt.Before(result.StartedAt) || result.DurationMillis < 0 {
		return errors.New("workflow result has invalid timing")
	}
	if _, allowed := definition(result.WorkflowID); !allowed {
		return fmt.Errorf("workflow result has non-allowlisted workflow %q", result.WorkflowID)
	}
	if result.Status == Succeeded && (result.ExitCode == nil || *result.ExitCode != 0) {
		return errors.New("succeeded workflow result must have exit code 0")
	}
	if (result.Status == Pending || result.Status == Running) && result.ExitCode != nil {
		return errors.New("active workflow result must not have an exit code")
	}
	if len(result.Output) > OutputLimit {
		return fmt.Errorf("workflow result output exceeds %d bytes", OutputLimit)
	}
	return nil
}
func (s *Store) save(r registry) (string, error) {
	sort.SliceStable(r.Results, func(i, j int) bool { return r.Results[i].StartedAt.After(r.Results[j].StartedAt) })
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	backup, err := storage.Backup(s.path, s.backups, "workflows.json")
	if err != nil {
		return "", err
	}
	if err := storage.WriteAtomic(s.path, data); err != nil {
		return backup, err
	}
	if err := pruneWorkflowBackups(s.backups); err != nil {
		return backup, err
	}
	return backup, nil
}

func pruneWorkflowBackups(directory string) error {
	matches, err := filepath.Glob(filepath.Join(directory, "workflows.json-*"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	if len(matches) <= backupLimit {
		return nil
	}
	for _, path := range matches[:len(matches)-backupLimit] {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("prune workflow backup: %w", err)
		}
	}
	return nil
}

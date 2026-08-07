package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

var storeLocks sync.Map

type Store struct {
	path string
	mu   *sync.Mutex
}

func NewStore(paths config.Paths) *Store {
	path := paths.ActivityFile
	if path == "" {
		path = filepath.Join(paths.StateDir, "activity.json")
	}
	lock, _ := storeLocks.LoadOrStore(path, &sync.Mutex{})
	return &Store{path: path, mu: lock.(*sync.Mutex)}
}

func (store *Store) List() ([]Event, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	registry, err := store.load()
	if err != nil {
		return nil, err
	}
	return append([]Event(nil), registry.Events...), nil
}

func (store *Store) Observe(observations []Observation) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	registry, err := store.load()
	if err != nil {
		return 0, err
	}
	emitted := 0
	dirty := false
	for _, observation := range observations {
		if err := validateObservation(observation); err != nil {
			return 0, err
		}
		previous, known := registry.States[observation.Key]
		if !known || previous != observation.State {
			dirty = true
		}
		registry.States[observation.Key] = observation.State
		if (known && previous == observation.State) || (!known && !observation.EmitInitial) {
			continue
		}
		registry.Events = append(registry.Events, eventFrom(observation))
		emitted++
	}
	if !dirty {
		return 0, nil
	}
	sort.SliceStable(registry.Events, func(i, j int) bool {
		return registry.Events[i].OccurredAt.After(registry.Events[j].OccurredAt)
	})
	if len(registry.Events) > HistoryLimit {
		registry.Events = registry.Events[:HistoryLimit]
	}
	if err := store.save(registry); err != nil {
		return 0, err
	}
	return emitted, nil
}

func (store *Store) load() (Registry, error) {
	registry := Registry{SchemaVersion: SchemaVersion, Events: []Event{}, States: map[string]string{}}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return registry, nil
		}
		return Registry{}, fmt.Errorf("read activity history: %w", err)
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return Registry{}, fmt.Errorf("decode activity history: %w", err)
	}
	if registry.SchemaVersion != SchemaVersion {
		return Registry{}, fmt.Errorf("unsupported activity schema version %d", registry.SchemaVersion)
	}
	if registry.Events == nil {
		registry.Events = []Event{}
	}
	if registry.States == nil {
		registry.States = map[string]string{}
	}
	return registry, nil
}

func (store *Store) save(registry Registry) error {
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode activity history: %w", err)
	}
	data = append(data, '\n')
	if err := storage.WriteAtomic(store.path, data); err != nil {
		return fmt.Errorf("save activity history: %w", err)
	}
	return nil
}

func validateObservation(observation Observation) error {
	if strings.TrimSpace(observation.Key) == "" || strings.TrimSpace(observation.Kind) == "" || strings.TrimSpace(observation.Title) == "" || strings.TrimSpace(observation.ResourceID) == "" || strings.TrimSpace(observation.State) == "" || observation.OccurredAt.IsZero() {
		return errors.New("activity observations require key, kind, title, resource ID, state, and occurrence time")
	}
	switch observation.Severity {
	case "info", "warning", "error":
		return nil
	default:
		return fmt.Errorf("invalid activity severity %q", observation.Severity)
	}
}

func eventFrom(observation Observation) Event {
	occurredAt := observation.OccurredAt.UTC()
	digest := sha256.Sum256([]byte(observation.Key + "\x00" + observation.State + "\x00" + occurredAt.Format(time.RFC3339Nano)))
	return Event{
		ID: hex.EncodeToString(digest[:8]), Kind: observation.Kind, Severity: observation.Severity,
		Title: observation.Title, ResourceID: observation.ResourceID, ProjectID: observation.ProjectID,
		State: observation.State, OccurredAt: occurredAt,
	}
}

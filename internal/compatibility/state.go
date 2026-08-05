package compatibility

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jisung9870/workbench/internal/storage"
)

const SchemaVersion = 1

type Observation struct {
	SchemaVersion  int       `json:"schema_version"`
	Client         string    `json:"client"`
	Feature        string    `json:"feature"`
	Source         string    `json:"source"`
	LastObservedAt time.Time `json:"last_observed_at"`
}

type allowedTuple struct {
	Client   string
	Feature  string
	Source   string
	External bool
}

var allowedTuples = []allowedTuple{
	{Client: "nvim", Feature: "projects", Source: "workbench", External: true},
	{Client: "nvim", Feature: "projects", Source: "binbox", External: true},
	{Client: "nvim", Feature: "projects", Source: "sessionizer", External: true},
	{Client: "workbench", Feature: "agents", Source: "registry"},
	{Client: "binbox", Feature: "agents", Source: "scrape", External: true},
}

type Store struct {
	directory string
}

func NewStore(directory string) *Store {
	return &Store{directory: directory}
}

func ValidateExternal(client, feature, source string) error {
	tuple, ok := findTuple(client, feature, source)
	if !ok || !tuple.External {
		return fmt.Errorf("unsupported external compatibility observation %s/%s/%s", client, feature, source)
	}
	return nil
}

func (store *Store) Observe(client, feature, source string, observedAt time.Time) error {
	tuple, ok := findTuple(client, feature, source)
	if !ok {
		return fmt.Errorf("unsupported compatibility observation %s/%s/%s", client, feature, source)
	}
	if observedAt.IsZero() {
		return fmt.Errorf("compatibility observation timestamp is required")
	}
	observation := Observation{
		SchemaVersion: SchemaVersion, Client: tuple.Client, Feature: tuple.Feature, Source: tuple.Source,
		LastObservedAt: observedAt.UTC(),
	}
	data, err := json.MarshalIndent(observation, "", "  ")
	if err != nil {
		return fmt.Errorf("encode compatibility observation: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		return fmt.Errorf("create compatibility state directory: %w", err)
	}
	if err := os.Chmod(store.directory, 0o700); err != nil {
		return fmt.Errorf("set compatibility state directory mode: %w", err)
	}
	if err := storage.WriteAtomic(store.path(tuple), data); err != nil {
		return fmt.Errorf("write compatibility observation: %w", err)
	}
	return nil
}

func (store *Store) Load() ([]Observation, error) {
	observations := []Observation{}
	for _, tuple := range allowedTuples {
		data, err := os.ReadFile(store.path(tuple))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read compatibility observation %s/%s/%s: %w", tuple.Client, tuple.Feature, tuple.Source, err)
		}
		observation, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("decode compatibility observation %s/%s/%s: %w", tuple.Client, tuple.Feature, tuple.Source, err)
		}
		if observation.SchemaVersion != SchemaVersion {
			return nil, fmt.Errorf("compatibility observation %s/%s/%s has unsupported schema version %d", tuple.Client, tuple.Feature, tuple.Source, observation.SchemaVersion)
		}
		if observation.Client != tuple.Client || observation.Feature != tuple.Feature || observation.Source != tuple.Source {
			return nil, fmt.Errorf("compatibility observation file does not match tuple %s/%s/%s", tuple.Client, tuple.Feature, tuple.Source)
		}
		if observation.LastObservedAt.IsZero() {
			return nil, fmt.Errorf("compatibility observation %s/%s/%s has no timestamp", tuple.Client, tuple.Feature, tuple.Source)
		}
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool {
		left := observations[i].Client + "\x00" + observations[i].Feature + "\x00" + observations[i].Source
		right := observations[j].Client + "\x00" + observations[j].Feature + "\x00" + observations[j].Source
		return left < right
	})
	return observations, nil
}

func (store *Store) path(tuple allowedTuple) string {
	return filepath.Join(store.directory, tuple.Client+"-"+tuple.Feature+"-"+tuple.Source+".json")
}

func findTuple(client, feature, source string) (allowedTuple, bool) {
	for _, tuple := range allowedTuples {
		if tuple.Client == client && tuple.Feature == feature && tuple.Source == source {
			return tuple, true
		}
	}
	return allowedTuple{}, false
}

func decode(data []byte) (Observation, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var observation Observation
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Observation{}, fmt.Errorf("unexpected trailing JSON value")
		}
		return Observation{}, err
	}
	return observation, nil
}

package environments

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

const SchemaVersion = 1

type Environment struct {
	ID            string            `toml:"id" json:"id"`
	AWSProfile    string            `toml:"aws_profile,omitempty" json:"aws_profile,omitempty"`
	AWSRegion     string            `toml:"aws_region,omitempty" json:"aws_region,omitempty"`
	KubeContext   string            `toml:"kube_context,omitempty" json:"kube_context,omitempty"`
	KubeNamespace string            `toml:"kube_namespace,omitempty" json:"kube_namespace,omitempty"`
	Exports       map[string]string `toml:"exports,omitempty" json:"exports"`
	Secrets       map[string]string `toml:"secrets,omitempty" json:"secrets"`
	ExpiresAt     *time.Time        `toml:"expires_at,omitempty" json:"expires_at,omitempty"`
}

const ExpiringWindow = 24 * time.Hour

type ExpiryStatus string

const (
	ExpiryPermanent ExpiryStatus = "permanent"
	ExpiryActive    ExpiryStatus = "active"
	ExpiryExpiring  ExpiryStatus = "expiring"
	ExpiryExpired   ExpiryStatus = "expired"
)

type Expiry struct {
	Status           ExpiryStatus `json:"status"`
	ExpiresAt        *time.Time   `json:"expires_at,omitempty"`
	RemainingSeconds int64        `json:"remaining_seconds,omitempty"`
}

func ExpiryAt(environment Environment, now time.Time) Expiry {
	if environment.ExpiresAt == nil {
		return Expiry{Status: ExpiryPermanent}
	}
	expiresAt := environment.ExpiresAt.UTC()
	result := Expiry{Status: ExpiryActive, ExpiresAt: &expiresAt}
	remaining := expiresAt.Sub(now.UTC())
	if remaining <= 0 {
		result.Status = ExpiryExpired
		return result
	}
	result.RemainingSeconds = int64(remaining / time.Second)
	if remaining <= ExpiringWindow {
		result.Status = ExpiryExpiring
	}
	return result
}

type Registry struct {
	SchemaVersion int           `toml:"schema_version" json:"schema_version"`
	Environments  []Environment `toml:"environments" json:"environments"`
}

type Store struct{ paths config.Paths }

type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

func NewStore(paths config.Paths) *Store { return &Store{paths: paths} }

func (s *Store) Load() (Registry, error) {
	registry := Registry{SchemaVersion: SchemaVersion, Environments: []Environment{}}
	metadata, err := toml.DecodeFile(s.paths.EnvironmentsFile, &registry)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return registry, nil
		}
		return Registry{}, fmt.Errorf("%s: %w", s.paths.EnvironmentsFile, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return Registry{}, fmt.Errorf("%s: unknown field %q", s.paths.EnvironmentsFile, undecoded[0].String())
	}
	if registry.Environments == nil {
		registry.Environments = []Environment{}
	}
	for i := range registry.Environments {
		if registry.Environments[i].Exports == nil {
			registry.Environments[i].Exports = map[string]string{}
		}
		if registry.Environments[i].Secrets == nil {
			registry.Environments[i].Secrets = map[string]string{}
		}
	}
	if err := ValidateRegistry(registry); err != nil {
		return Registry{}, fmt.Errorf("%s: %w", s.paths.EnvironmentsFile, err)
	}
	sort.Slice(registry.Environments, func(i, j int) bool { return registry.Environments[i].ID < registry.Environments[j].ID })
	return registry, nil
}

func (s *Store) List() ([]Environment, error) {
	registry, err := s.Load()
	return registry.Environments, err
}

func (s *Store) Show(id string) (Environment, bool, error) {
	registry, err := s.Load()
	if err != nil {
		return Environment{}, false, err
	}
	for _, environment := range registry.Environments {
		if environment.ID == id {
			return environment, true, nil
		}
	}
	return Environment{}, false, nil
}

func (s *Store) Add(environment Environment) (string, error) {
	registry, err := s.Load()
	if err != nil {
		return "", err
	}
	for _, existing := range registry.Environments {
		if existing.ID == environment.ID {
			return "", &ConflictError{Message: fmt.Sprintf("environment id %q already exists", environment.ID)}
		}
	}
	registry.Environments = append(registry.Environments, normalized(environment))
	return s.Save(registry)
}

func (s *Store) AddMany(environments []Environment) (string, error) {
	registry, err := s.Load()
	if err != nil {
		return "", err
	}
	registry.Environments = append(registry.Environments, environments...)
	return s.Save(registry)
}

func (s *Store) Remove(id string) (Environment, bool, string, error) {
	registry, err := s.Load()
	if err != nil {
		return Environment{}, false, "", err
	}
	for i, environment := range registry.Environments {
		if environment.ID != id {
			continue
		}
		registry.Environments = append(registry.Environments[:i], registry.Environments[i+1:]...)
		backup, saveErr := s.Save(registry)
		return environment, true, backup, saveErr
	}
	return Environment{}, false, "", nil
}

func (s *Store) SetExpiry(id string, expiresAt *time.Time) (Environment, bool, string, error) {
	registry, err := s.Load()
	if err != nil {
		return Environment{}, false, "", err
	}
	for index := range registry.Environments {
		if registry.Environments[index].ID != id {
			continue
		}
		if expiresAt == nil {
			registry.Environments[index].ExpiresAt = nil
		} else {
			utc := expiresAt.UTC()
			registry.Environments[index].ExpiresAt = &utc
		}
		backup, saveErr := s.Save(registry)
		return registry.Environments[index], true, backup, saveErr
	}
	return Environment{}, false, "", nil
}

func (s *Store) Save(registry Registry) (string, error) {
	for i := range registry.Environments {
		registry.Environments[i] = normalized(registry.Environments[i])
	}
	sort.Slice(registry.Environments, func(i, j int) bool { return registry.Environments[i].ID < registry.Environments[j].ID })
	if err := ValidateRegistry(registry); err != nil {
		return "", err
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(registry); err != nil {
		return "", fmt.Errorf("encode environment registry: %w", err)
	}
	var roundTrip Registry
	metadata, err := toml.Decode(encoded.String(), &roundTrip)
	if err != nil {
		return "", fmt.Errorf("validate encoded environment registry: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return "", fmt.Errorf("validate encoded environment registry: unknown field %q", undecoded[0].String())
	}
	if err := ValidateRegistry(roundTrip); err != nil {
		return "", fmt.Errorf("validate encoded environment registry: %w", err)
	}
	backup, err := storage.Backup(s.paths.EnvironmentsFile, s.paths.BackupsDir, "environments.toml")
	if err != nil {
		return "", err
	}
	if err := storage.WriteAtomic(s.paths.EnvironmentsFile, encoded.Bytes()); err != nil {
		return backup, err
	}
	if _, err := s.Load(); err != nil {
		return backup, fmt.Errorf("validate written environment registry: %w", err)
	}
	return backup, nil
}

func ValidateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion {
		return &InvalidError{Message: fmt.Sprintf("unsupported schema_version %d (expected %d)", registry.SchemaVersion, SchemaVersion)}
	}
	ids := map[string]struct{}{}
	for index, environment := range registry.Environments {
		if !ValidID(environment.ID) {
			return &InvalidError{Message: fmt.Sprintf("environments[%d].id %q is invalid", index, environment.ID)}
		}
		if _, exists := ids[environment.ID]; exists {
			return &ConflictError{Message: fmt.Sprintf("duplicate environment id %q", environment.ID)}
		}
		ids[environment.ID] = struct{}{}
		if environment.ExpiresAt != nil && environment.ExpiresAt.IsZero() {
			return &InvalidError{Message: fmt.Sprintf("environment %q expires_at must be a valid timestamp", environment.ID)}
		}
		for key := range environment.Exports {
			if !ValidVariableName(key) {
				return &InvalidError{Message: fmt.Sprintf("environment %q export key %q is invalid", environment.ID, key)}
			}
			if ReservedKey(key) {
				return &InvalidError{Message: fmt.Sprintf("environment %q export key %q must use its dedicated field", environment.ID, key)}
			}
		}
		for key, reference := range environment.Secrets {
			if !ValidVariableName(key) {
				return &InvalidError{Message: fmt.Sprintf("environment %q secret key %q is invalid", environment.ID, key)}
			}
			if ReservedKey(key) {
				return &InvalidError{Message: fmt.Sprintf("environment %q secret key %q conflicts with a dedicated field", environment.ID, key)}
			}
			if _, exists := environment.Exports[key]; exists {
				return &ConflictError{Message: fmt.Sprintf("environment %q variable %q is defined by both exports and secrets", environment.ID, key)}
			}
			if _, err := ParseSecretReference(reference); err != nil {
				return &InvalidError{Message: fmt.Sprintf("environment %q secret key %q: %s", environment.ID, key, err)}
			}
		}
	}
	return nil
}

func ValidID(id string) bool {
	if id == "" {
		return false
	}
	for index, character := range id {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func ValidVariableName(key string) bool {
	if key == "" {
		return false
	}
	for index, character := range key {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func ReservedKey(key string) bool {
	switch key {
	case "AWS_PROFILE", "AWS_REGION", "KUBE_CONTEXT", "KUBE_NAMESPACE":
		return true
	default:
		return false
	}
}

func ExportValues(environment Environment) map[string]string {
	values := map[string]string{}
	if environment.AWSProfile != "" {
		values["AWS_PROFILE"] = environment.AWSProfile
	}
	if environment.AWSRegion != "" {
		values["AWS_REGION"] = environment.AWSRegion
	}
	for key, value := range environment.Exports {
		values[key] = value
	}
	return values
}

func ShellExports(environment Environment) string {
	values := ExportValues(environment)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&result, "export %s=%s\n", key, shellQuote(values[key]))
	}
	return result.String()
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func normalized(environment Environment) Environment {
	if environment.Exports == nil {
		environment.Exports = map[string]string{}
	}
	if environment.Secrets == nil {
		environment.Secrets = map[string]string{}
	}
	if environment.ExpiresAt != nil {
		utc := environment.ExpiresAt.UTC()
		environment.ExpiresAt = &utc
	}
	return environment
}

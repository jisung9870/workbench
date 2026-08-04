package projects

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

const SchemaVersion = 1

type Project struct {
	ID             string   `toml:"id" json:"id"`
	Name           string   `toml:"name" json:"name"`
	Path           string   `toml:"path" json:"path"`
	RepoRoot       string   `toml:"repo_root" json:"repo_root"`
	DefaultBackend string   `toml:"default_backend" json:"default_backend"`
	Editor         string   `toml:"editor" json:"editor"`
	Tags           []string `toml:"tags" json:"tags"`
	Profile        string   `toml:"profile" json:"profile"`
}

type Registry struct {
	SchemaVersion int       `toml:"schema_version"`
	Projects      []Project `toml:"projects"`
}

type Store struct {
	paths config.Paths
}

func NewStore(paths config.Paths) *Store {
	return &Store{paths: paths}
}

func (s *Store) Paths() config.Paths {
	return s.paths
}

func (s *Store) Load() (Registry, error) {
	registry := Registry{Projects: []Project{}}
	metadata, err := toml.DecodeFile(s.paths.ProjectsFile, &registry)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{SchemaVersion: SchemaVersion, Projects: []Project{}}, nil
		}
		return Registry{}, fmt.Errorf("%s: %w", s.paths.ProjectsFile, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return Registry{}, fmt.Errorf("%s: unknown field %q", s.paths.ProjectsFile, undecoded[0].String())
	}
	if registry.Projects == nil {
		registry.Projects = []Project{}
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, fmt.Errorf("%s: %w", s.paths.ProjectsFile, err)
	}
	sort.Slice(registry.Projects, func(i, j int) bool { return registry.Projects[i].ID < registry.Projects[j].ID })
	return registry, nil
}

func (s *Store) List() ([]Project, error) {
	registry, err := s.Load()
	if err != nil {
		return nil, err
	}
	return registry.Projects, nil
}

func (s *Store) Show(id string) (Project, bool, error) {
	registry, err := s.Load()
	if err != nil {
		return Project{}, false, err
	}
	for _, project := range registry.Projects {
		if project.ID == id {
			return project, true, nil
		}
	}
	return Project{}, false, nil
}

func (s *Store) Add(path, id, profile string) (Project, string, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return Project{}, "", err
	}
	if id == "" {
		id = DeriveID(filepath.Base(canonical))
	}
	if profile == "" {
		profile = "personal"
	}
	project := Project{
		ID:             id,
		Name:           filepath.Base(canonical),
		Path:           canonical,
		RepoRoot:       canonical,
		DefaultBackend: "auto",
		Editor:         "nvim",
		Tags:           []string{},
		Profile:        profile,
	}
	backup, err := s.AddMany([]Project{project})
	return project, backup, err
}

func (s *Store) AddMany(newProjects []Project) (string, error) {
	registry, err := s.Load()
	if err != nil {
		return "", err
	}
	registry.Projects = append(registry.Projects, newProjects...)
	if err := validateRegistry(registry); err != nil {
		return "", err
	}
	return s.save(registry)
}

func (s *Store) Remove(id string) (Project, bool, string, error) {
	registry, err := s.Load()
	if err != nil {
		return Project{}, false, "", err
	}
	for index, project := range registry.Projects {
		if project.ID != id {
			continue
		}
		registry.Projects = append(registry.Projects[:index], registry.Projects[index+1:]...)
		backup, err := s.save(registry)
		return project, true, backup, err
	}
	return Project{}, false, "", nil
}

func (s *Store) save(registry Registry) (string, error) {
	sort.Slice(registry.Projects, func(i, j int) bool { return registry.Projects[i].ID < registry.Projects[j].ID })
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(registry); err != nil {
		return "", fmt.Errorf("encode project registry: %w", err)
	}
	var roundTrip Registry
	metadata, err := toml.Decode(encoded.String(), &roundTrip)
	if err != nil {
		return "", fmt.Errorf("validate encoded project registry: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return "", fmt.Errorf("validate encoded project registry: unknown field %q", undecoded[0].String())
	}
	backup, err := storage.Backup(s.paths.ProjectsFile, s.paths.BackupsDir, "projects.toml")
	if err != nil {
		return "", err
	}
	if err := storage.WriteAtomic(s.paths.ProjectsFile, encoded.Bytes()); err != nil {
		return backup, err
	}
	if _, err := s.Load(); err != nil {
		return backup, fmt.Errorf("validate written project registry: %w", err)
	}
	return backup, nil
}

func CanonicalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("project path must not be empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute project path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve project path %q: %w", path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect project path %q: %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", canonical)
	}
	return filepath.Clean(canonical), nil
}

func DeriveID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	lastDash := false
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(builder.String(), "-._")
	if id == "" {
		return "project"
	}
	return id
}

func validateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (expected %d)", registry.SchemaVersion, SchemaVersion)
	}
	ids := map[string]struct{}{}
	paths := map[string]string{}
	for index, project := range registry.Projects {
		if !validID(project.ID) {
			return fmt.Errorf("projects[%d].id %q is invalid", index, project.ID)
		}
		if _, exists := ids[project.ID]; exists {
			return fmt.Errorf("duplicate project id %q", project.ID)
		}
		ids[project.ID] = struct{}{}
		if project.Path == "" || !filepath.IsAbs(project.Path) {
			return fmt.Errorf("project %q path must be absolute", project.ID)
		}
		pathKey := canonicalKey(project.Path)
		if owner, exists := paths[pathKey]; exists {
			return fmt.Errorf("canonical path %q is already owned by project %q", project.Path, owner)
		}
		paths[pathKey] = project.ID
		if project.Profile == "" {
			return fmt.Errorf("project %q profile must not be empty", project.ID)
		}
	}
	return nil
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for index, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func canonicalKey(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

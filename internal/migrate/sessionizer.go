package migrate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/storage"
)

type SessionizerPlan struct {
	Source   string
	Projects []projects.Project
	Skipped  []string
	Warnings []string
}

func PlanSessionizer(source, profile string, store *projects.Store) (SessionizerPlan, error) {
	if profile == "" {
		profile = "personal"
	}
	discovered, warnings, err := discoverSessionizer(source)
	if err != nil {
		return SessionizerPlan{}, err
	}
	existing, err := store.List()
	if err != nil {
		return SessionizerPlan{}, err
	}
	pathOwners := map[string]string{}
	usedIDs := map[string]struct{}{}
	for _, project := range existing {
		pathOwners[filepath.Clean(project.Path)] = project.ID
		usedIDs[project.ID] = struct{}{}
	}
	plan := SessionizerPlan{Source: source, Projects: []projects.Project{}, Skipped: []string{}, Warnings: warnings}
	for _, path := range discovered {
		if owner, exists := pathOwners[filepath.Clean(path)]; exists {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s (already registered as %s)", path, owner))
			continue
		}
		baseID := projects.DeriveID(filepath.Base(path))
		id := baseID
		for suffix := 2; ; suffix++ {
			if _, exists := usedIDs[id]; !exists {
				break
			}
			id = fmt.Sprintf("%s-%d", baseID, suffix)
		}
		usedIDs[id] = struct{}{}
		pathOwners[filepath.Clean(path)] = id
		plan.Projects = append(plan.Projects, projects.Project{
			ID:             id,
			Name:           filepath.Base(path),
			Path:           path,
			RepoRoot:       path,
			DefaultBackend: "auto",
			Editor:         "nvim",
			Tags:           []string{},
			Profile:        profile,
		})
	}
	return plan, nil
}

func ApplySessionizer(plan SessionizerPlan, store *projects.Store) ([]string, error) {
	backups := []string{}
	sourceBackup, err := storage.Backup(plan.Source, store.Paths().BackupsDir, "sessionizer-dirs")
	if err != nil {
		return nil, err
	}
	if sourceBackup != "" {
		backups = append(backups, sourceBackup)
	}
	if len(plan.Projects) == 0 {
		return backups, nil
	}
	registryBackup, err := store.AddMany(plan.Projects)
	if err != nil {
		return backups, err
	}
	if registryBackup != "" {
		backups = append(backups, registryBackup)
	}
	return backups, nil
}

func discoverSessionizer(source string) ([]string, []string, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, nil, fmt.Errorf("open sessionizer source %s: %w", source, err)
	}
	defer file.Close()
	seen := map[string]struct{}{}
	paths := []string{}
	warnings := []string{}
	add := func(candidate string) {
		canonical, err := projects.CanonicalPath(candidate)
		if err != nil {
			warnings = append(warnings, err.Error())
			return
		}
		if _, exists := seen[canonical]; exists {
			return
		}
		seen[canonical] = struct{}{}
		paths = append(paths, canonical)
	}
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		direct := strings.HasPrefix(line, "=")
		if direct {
			line = strings.TrimSpace(strings.TrimPrefix(line, "="))
		}
		line = expandHome(line)
		if direct {
			add(line)
			continue
		}
		entries, err := os.ReadDir(line)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: read %q: %v", source, lineNumber, line, err))
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			candidate := filepath.Join(line, entry.Name())
			info, err := os.Stat(candidate)
			if err == nil && info.IsDir() {
				add(candidate)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read sessionizer source %s: %w", source, err)
	}
	sort.Strings(paths)
	return paths, warnings, nil
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
}

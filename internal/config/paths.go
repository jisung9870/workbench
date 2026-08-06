package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Paths struct {
	ConfigDir        string
	StateDir         string
	ConfigFile       string
	ProjectsFile     string
	WorktreesFile    string
	AgentsFile       string
	WorkflowsFile    string
	CompatibilityDir string
	ProfilesDir      string
	BackupsDir       string
}

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	configBase, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
	}
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user cache directory: %w", err)
	}
	return resolvePaths(runtime.GOOS, home, configBase, cacheBase, os.Getenv)
}

func resolvePaths(goos, home, userConfig, userCache string, getenv func(string) string) (Paths, error) {
	var configBase, stateBase string
	if goos == "windows" {
		configBase = getenv("APPDATA")
		if configBase == "" {
			configBase = userConfig
		}
		stateBase = getenv("LOCALAPPDATA")
		if stateBase == "" {
			stateBase = userCache
		}
	} else {
		configBase = getenv("XDG_CONFIG_HOME")
		if configBase == "" {
			configBase = filepath.Join(home, ".config")
		}
		stateBase = getenv("XDG_STATE_HOME")
		if stateBase == "" {
			stateBase = filepath.Join(home, ".local", "state")
		}
	}
	if !filepath.IsAbs(configBase) {
		return Paths{}, fmt.Errorf("config base must be absolute: %q", configBase)
	}
	if !filepath.IsAbs(stateBase) {
		return Paths{}, fmt.Errorf("state base must be absolute: %q", stateBase)
	}
	configDir := filepath.Join(configBase, "workbench")
	stateDir := filepath.Join(stateBase, "workbench")
	return Paths{
		ConfigDir:        configDir,
		StateDir:         stateDir,
		ConfigFile:       filepath.Join(configDir, "config.toml"),
		ProjectsFile:     filepath.Join(configDir, "projects.toml"),
		WorktreesFile:    filepath.Join(stateDir, "worktrees.json"),
		AgentsFile:       filepath.Join(stateDir, "agents.json"),
		WorkflowsFile:    filepath.Join(stateDir, "workflows.json"),
		CompatibilityDir: filepath.Join(stateDir, "compatibility"),
		ProfilesDir:      filepath.Join(configDir, "profiles"),
		BackupsDir:       filepath.Join(stateDir, "backups"),
	}, nil
}

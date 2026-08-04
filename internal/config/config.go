package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const SchemaVersion = 1

type Settings struct {
	SchemaVersion int    `toml:"schema_version"`
	ActiveProfile string `toml:"active_profile"`
}

type Profile struct {
	SchemaVersion          int    `toml:"schema_version"`
	DefaultBackend         string `toml:"default_backend"`
	Editor                 string `toml:"editor"`
	WindowsTerminalProfile string `toml:"windows_terminal_profile"`
}

func DefaultSettings() Settings {
	return Settings{SchemaVersion: SchemaVersion, ActiveProfile: "personal"}
}

func LoadSettings(path string) (Settings, error) {
	settings := Settings{}
	if err := decodeExactFile(path, &settings); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return Settings{}, err
	}
	if settings.SchemaVersion != SchemaVersion {
		return Settings{}, fmt.Errorf("%s: unsupported schema_version %d (expected %d)", path, settings.SchemaVersion, SchemaVersion)
	}
	if settings.ActiveProfile == "" {
		return Settings{}, fmt.Errorf("%s: active_profile must not be empty", path)
	}
	if !ValidProfileName(settings.ActiveProfile) {
		return Settings{}, fmt.Errorf("%s: invalid active_profile %q", path, settings.ActiveProfile)
	}
	return settings, nil
}

func DefaultProfile() Profile {
	return Profile{SchemaVersion: SchemaVersion, DefaultBackend: "auto", Editor: "nvim"}
}

func LoadProfile(paths Paths, name string) (Profile, error) {
	if name == "" {
		return DefaultProfile(), nil
	}
	if !ValidProfileName(name) {
		return Profile{}, fmt.Errorf("invalid profile name %q", name)
	}
	profile := Profile{}
	path := filepath.Join(paths.ProfilesDir, name+".toml")
	if err := decodeExactFile(path, &profile); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultProfile(), nil
		}
		return Profile{}, err
	}
	if profile.SchemaVersion != SchemaVersion {
		return Profile{}, fmt.Errorf("%s: unsupported schema_version %d (expected %d)", path, profile.SchemaVersion, SchemaVersion)
	}
	if profile.DefaultBackend == "" {
		profile.DefaultBackend = "auto"
	}
	if profile.Editor == "" {
		profile.Editor = "nvim"
	}
	if !ValidBackend(profile.DefaultBackend) {
		return Profile{}, fmt.Errorf("%s: invalid default_backend %q", path, profile.DefaultBackend)
	}
	return profile, nil
}

func ValidProfileName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func ValidBackend(name string) bool {
	switch name {
	case "auto", "cmux", "windows-terminal", "tmux", "shell":
		return true
	default:
		return false
	}
}

func Validate(paths Paths) error {
	if _, err := LoadSettings(paths.ConfigFile); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.ProfilesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read profiles directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		path := filepath.Join(paths.ProfilesDir, entry.Name())
		profile := Profile{}
		if err := decodeExactFile(path, &profile); err != nil {
			return err
		}
		if profile.SchemaVersion != SchemaVersion {
			return fmt.Errorf("%s: unsupported schema_version %d (expected %d)", path, profile.SchemaVersion, SchemaVersion)
		}
		if profile.DefaultBackend != "" && !ValidBackend(profile.DefaultBackend) {
			return fmt.Errorf("%s: invalid default_backend %q", path, profile.DefaultBackend)
		}
	}
	return nil
}

func decodeExactFile(path string, value any) error {
	metadata, err := toml.DecodeFile(path, value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("%s: unknown field %q", path, undecoded[0].String())
	}
	return nil
}

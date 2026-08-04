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
	SchemaVersion  int    `toml:"schema_version"`
	DefaultBackend string `toml:"default_backend"`
	Editor         string `toml:"editor"`
}

func DefaultSettings() Settings {
	return Settings{SchemaVersion: SchemaVersion, ActiveProfile: "personal"}
}

func LoadSettings(path string) (Settings, error) {
	settings := Settings{}
	if err := decodeExactFile(path, &settings); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return settings, nil
		}
		return Settings{}, err
	}
	if settings.SchemaVersion != SchemaVersion {
		return Settings{}, fmt.Errorf("%s: unsupported schema_version %d (expected %d)", path, settings.SchemaVersion, SchemaVersion)
	}
	if settings.ActiveProfile == "" {
		return Settings{}, fmt.Errorf("%s: active_profile must not be empty", path)
	}
	return settings, nil
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

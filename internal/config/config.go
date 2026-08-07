package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jisung9870/workbench/internal/storage"
)

const SchemaVersion = 1

type Settings struct {
	SchemaVersion int    `toml:"schema_version"`
	ActiveProfile string `toml:"active_profile"`
}

type Profile struct {
	SchemaVersion          int      `toml:"schema_version" json:"schema_version"`
	DefaultBackend         string   `toml:"default_backend" json:"default_backend"`
	PreferCurrentTmux      bool     `toml:"prefer_current_tmux" json:"prefer_current_tmux"`
	BackendPriority        []string `toml:"backend_priority" json:"backend_priority"`
	Editor                 string   `toml:"editor" json:"editor"`
	WindowsTerminalProfile string   `toml:"windows_terminal_profile" json:"windows_terminal_profile"`
	WindowsTerminalDistro  string   `toml:"windows_terminal_distro" json:"windows_terminal_distro"`
	WindowsTerminalWindow  string   `toml:"windows_terminal_window" json:"windows_terminal_window"`
	WindowsTerminalMode    string   `toml:"windows_terminal_mode" json:"windows_terminal_mode"`
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
	return Profile{
		SchemaVersion: SchemaVersion, DefaultBackend: "auto", PreferCurrentTmux: true, Editor: "nvim",
		WindowsTerminalWindow: "last", WindowsTerminalMode: "tab",
	}
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
	metadata, err := decodeExactFileMetadata(path, &profile)
	if err != nil {
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
	if !metadata.IsDefined("prefer_current_tmux") {
		profile.PreferCurrentTmux = true
	}
	if profile.Editor == "" {
		profile.Editor = "nvim"
	}
	if profile.WindowsTerminalWindow == "" {
		profile.WindowsTerminalWindow = "last"
	}
	if profile.WindowsTerminalMode == "" {
		profile.WindowsTerminalMode = "tab"
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	return profile, nil
}

func SaveProfile(paths Paths, name string, profile Profile) (string, error) {
	if !ValidProfileName(name) {
		return "", fmt.Errorf("invalid profile name %q", name)
	}
	profile.SchemaVersion = SchemaVersion
	if err := ValidateProfile(profile); err != nil {
		return "", err
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(profile); err != nil {
		return "", fmt.Errorf("encode profile %q: %w", name, err)
	}
	path := filepath.Join(paths.ProfilesDir, name+".toml")
	backup, err := storage.Backup(path, paths.BackupsDir, "profile-"+name+".toml")
	if err != nil {
		return "", err
	}
	if err := storage.WriteAtomic(path, encoded.Bytes()); err != nil {
		return backup, err
	}
	if _, err := LoadProfile(paths, name); err != nil {
		return backup, fmt.Errorf("validate written profile %q: %w", name, err)
	}
	return backup, nil
}

func ValidateProfile(profile Profile) error {
	if profile.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (expected %d)", profile.SchemaVersion, SchemaVersion)
	}
	if !ValidBackend(profile.DefaultBackend) {
		return fmt.Errorf("invalid default_backend %q", profile.DefaultBackend)
	}
	if err := ValidateBackendPriority(profile.BackendPriority); err != nil {
		return err
	}
	if profile.Editor == "" || hasControl(profile.Editor) {
		return errors.New("editor must not be empty or contain control characters")
	}
	if hasControl(profile.WindowsTerminalProfile) {
		return errors.New("windows_terminal_profile contains control characters")
	}
	if !ValidWindowsTerminalWindow(profile.WindowsTerminalWindow) {
		return fmt.Errorf("invalid windows_terminal_window %q", profile.WindowsTerminalWindow)
	}
	if !ValidWindowsTerminalMode(profile.WindowsTerminalMode) {
		return fmt.Errorf("invalid windows_terminal_mode %q", profile.WindowsTerminalMode)
	}
	if !ValidWindowsTerminalDistro(profile.WindowsTerminalDistro) {
		return fmt.Errorf("invalid windows_terminal_distro %q", profile.WindowsTerminalDistro)
	}
	return nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
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

func ValidateBackendPriority(values []string) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "auto" || !ValidBackend(value) {
			return fmt.Errorf("invalid backend_priority entry %q", value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate backend_priority entry %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
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
		if err := ValidateBackendPriority(profile.BackendPriority); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if profile.WindowsTerminalWindow != "" && !ValidWindowsTerminalWindow(profile.WindowsTerminalWindow) {
			return fmt.Errorf("%s: invalid windows_terminal_window %q", path, profile.WindowsTerminalWindow)
		}
		if profile.WindowsTerminalMode != "" && !ValidWindowsTerminalMode(profile.WindowsTerminalMode) {
			return fmt.Errorf("%s: invalid windows_terminal_mode %q", path, profile.WindowsTerminalMode)
		}
		if !ValidWindowsTerminalDistro(profile.WindowsTerminalDistro) {
			return fmt.Errorf("%s: invalid windows_terminal_distro %q", path, profile.WindowsTerminalDistro)
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, err := LoadProfile(paths, name); err != nil {
			return err
		}
	}
	return nil
}

func ValidWindowsTerminalWindow(value string) bool {
	value = strings.TrimSpace(value)
	if value == "last" || value == "new" {
		return true
	}
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func ValidWindowsTerminalMode(value string) bool {
	switch strings.TrimSpace(value) {
	case "tab", "split-auto", "split-horizontal", "split-vertical":
		return true
	default:
		return false
	}
}

func ValidWindowsTerminalDistro(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed != value || strings.HasPrefix(trimmed, "-") {
		return false
	}
	for _, character := range trimmed {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func decodeExactFile(path string, value any) error {
	_, err := decodeExactFileMetadata(path, value)
	return err
}

func decodeExactFileMetadata(path string, value any) (toml.MetaData, error) {
	metadata, err := toml.DecodeFile(path, value)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return toml.MetaData{}, fmt.Errorf("%s: unknown field %q", path, undecoded[0].String())
	}
	return metadata, nil
}

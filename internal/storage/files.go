package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func Backup(path, backupDir, label string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open backup source: %w", err)
	}
	defer source.Close()
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	name := fmt.Sprintf("%s-%s", label, time.Now().UTC().Format("20060102T150405.000000000Z"))
	destinationPath := filepath.Join(backupDir, name)
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return "", fmt.Errorf("copy backup: %w", err)
	}
	if err := destination.Sync(); err != nil {
		destination.Close()
		return "", fmt.Errorf("flush backup: %w", err)
	}
	if err := destination.Close(); err != nil {
		return "", fmt.Errorf("close backup: %w", err)
	}
	return destinationPath, nil
}

func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".workbench-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

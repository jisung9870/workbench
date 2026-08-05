package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func acquireRegistryFileLock(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create agent registry lock parent: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open agent registry lock: %w", err)
	}
	if err := lockRegistryFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock agent registry: %w", err)
	}
	return func() error {
		unlockErr := unlockRegistryFile(file)
		closeErr := file.Close()
		if unlockErr != nil || closeErr != nil {
			return fmt.Errorf("release agent registry lock: %w", errors.Join(unlockErr, closeErr))
		}
		return nil
	}, nil
}

package workflows

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func acquireHistoryFileLock(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create workflow history lock parent: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open workflow history lock: %w", err)
	}
	if err := lockHistoryFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock workflow history: %w", err)
	}
	return func() error {
		unlockErr := unlockHistoryFile(file)
		closeErr := file.Close()
		if unlockErr != nil || closeErr != nil {
			return fmt.Errorf("release workflow history lock: %w", errors.Join(unlockErr, closeErr))
		}
		return nil
	}, nil
}

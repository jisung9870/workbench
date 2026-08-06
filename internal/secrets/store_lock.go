package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func acquireSecretsFileLock(path string) (func() error, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create secrets lock parent: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("secure secrets lock parent: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open secrets lock: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, fmt.Errorf("secure secrets lock: %w", err)
		}
	}
	if err := lockSecretsFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock secrets store: %w", err)
	}
	return func() error {
		unlockErr := unlockSecretsFile(file)
		closeErr := file.Close()
		if unlockErr != nil || closeErr != nil {
			return fmt.Errorf("release secrets lock: %w", errors.Join(unlockErr, closeErr))
		}
		return nil
	}, nil
}

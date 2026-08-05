//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package agents

import (
	"errors"
	"os"
	"syscall"
)

func lockRegistryFile(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func unlockRegistryFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

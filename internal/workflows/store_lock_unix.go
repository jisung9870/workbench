//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workflows

import (
	"errors"
	"os"
	"syscall"
)

func lockHistoryFile(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
func unlockHistoryFile(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }

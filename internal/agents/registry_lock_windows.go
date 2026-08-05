//go:build windows

package agents

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const lockfileExclusiveLock = 0x00000002

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func lockRegistryFile(file *os.File) error {
	overlapped := syscall.Overlapped{}
	result, _, callErr := lockFileEx.Call(file.Fd(), lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("LockFileEx: %w", callErr)
	}
	return nil
}

func unlockRegistryFile(file *os.File) error {
	overlapped := syscall.Overlapped{}
	result, _, callErr := unlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("UnlockFileEx: %w", callErr)
	}
	return nil
}

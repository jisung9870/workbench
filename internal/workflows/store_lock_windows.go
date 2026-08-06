//go:build windows

package workflows

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const historyExclusiveLock = 0x00000002

var historyKernel32 = syscall.NewLazyDLL("kernel32.dll")
var historyLockFileEx = historyKernel32.NewProc("LockFileEx")
var historyUnlockFileEx = historyKernel32.NewProc("UnlockFileEx")

func lockHistoryFile(file *os.File) error {
	overlapped := syscall.Overlapped{}
	result, _, callErr := historyLockFileEx.Call(file.Fd(), historyExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("LockFileEx: %w", callErr)
	}
	return nil
}
func unlockHistoryFile(file *os.File) error {
	overlapped := syscall.Overlapped{}
	result, _, callErr := historyUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("UnlockFileEx: %w", callErr)
	}
	return nil
}

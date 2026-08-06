//go:build windows

package secrets

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const secretsExclusiveLock = 0x00000002

var secretsKernel32 = syscall.NewLazyDLL("kernel32.dll")
var secretsLockFileEx = secretsKernel32.NewProc("LockFileEx")
var secretsUnlockFileEx = secretsKernel32.NewProc("UnlockFileEx")

func lockSecretsFile(file *os.File) error {
	overlapped := syscall.Overlapped{}
	result, _, callErr := secretsLockFileEx.Call(file.Fd(), secretsExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("LockFileEx: %w", callErr)
	}
	return nil
}

func unlockSecretsFile(file *os.File) error {
	overlapped := syscall.Overlapped{}
	result, _, callErr := secretsUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("UnlockFileEx: %w", callErr)
	}
	return nil
}

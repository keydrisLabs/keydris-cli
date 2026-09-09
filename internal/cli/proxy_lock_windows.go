//go:build windows

package cli

import (
	"fmt"
	"syscall"
)

func lockProxyFile(path string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot lock proxy lifecycle (another command may be running): %w", err)
	}
	return func() { syscall.CloseHandle(handle) }, nil
}

//go:build linux

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

func terminalFile(f *os.File) bool {
	var term syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&term)))
	return err == 0
}

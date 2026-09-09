//go:build windows

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

func terminalFile(f *os.File) bool {
	dll := syscall.NewLazyDLL("kernel32.dll")
	var mode uint32
	ok, _, _ := dll.NewProc("GetConsoleMode").Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	if ok == 0 {
		return false
	}
	// Enable ANSI output on Windows consoles; input handles need no change.
	if f != os.Stdin {
		dll.NewProc("SetConsoleMode").Call(f.Fd(), uintptr(mode|0x0004))
	}
	return true
}

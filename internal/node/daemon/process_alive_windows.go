//go:build windows

package daemon

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

var (
	processKernel32    = syscall.NewLazyDLL("kernel32.dll")
	processOpen        = processKernel32.NewProc("OpenProcess")
	processExitCode    = processKernel32.NewProc("GetExitCodeProcess")
	processTimes       = processKernel32.NewProc("GetProcessTimes")
	processCloseHandle = processKernel32.NewProc("CloseHandle")
)

type processFiletime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

func ownerProcessMatches(pid int, expected string) (bool, error) {
	handle, _, openErr := processOpen.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		if errno, ok := openErr.(syscall.Errno); ok && (errno == 6 || errno == 87) {
			return false, nil
		}
		return false, fmt.Errorf("open owner pid %d: %v", pid, openErr)
	}
	defer processCloseHandle.Call(handle)

	var exitCode uint32
	ok, _, queryErr := processExitCode.Call(handle, uintptr(unsafe.Pointer(&exitCode)))
	if ok == 0 {
		return false, fmt.Errorf("query owner pid %d: %v", pid, queryErr)
	}
	if exitCode != stillActive {
		return false, nil
	}

	var created, exited, kernel, user processFiletime
	ok, _, timesErr := processTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&created)),
		uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ok == 0 {
		return false, fmt.Errorf("query owner pid %d creation time: %v", pid, timesErr)
	}
	identity := fmt.Sprintf("windows-start:%08x%08x", created.HighDateTime, created.LowDateTime)
	return identity == expected, nil
}

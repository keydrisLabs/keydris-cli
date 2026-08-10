//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	detachedProcess         = 0x00000008
	createNewProcessGroup   = 0x00000200
	processQueryLimitedInfo = 0x1000
)

var (
	kernel32Process = syscall.NewLazyDLL("kernel32.dll")
	openProcess     = kernel32Process.NewProc("OpenProcess")
	getProcessTimes = kernel32Process.NewProc("GetProcessTimes")
	closeProcess    = kernel32Process.NewProc("CloseHandle")
)

type windowsFiletime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
}

func stopProcess(proc *os.Process) error {
	return proc.Kill()
}

func processIdentity(pid int) (string, error) {
	handle, _, openErr := openProcess.Call(processQueryLimitedInfo, 0, uintptr(pid))
	if handle == 0 {
		return "", fmt.Errorf("open pid %d: %v", pid, openErr)
	}
	defer closeProcess.Call(handle)

	var created, exited, kernel, user windowsFiletime
	ok, _, timesErr := getProcessTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&created)),
		uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ok == 0 {
		return "", fmt.Errorf("query pid %d creation time: %v", pid, timesErr)
	}
	return fmt.Sprintf("windows-start:%08x%08x", created.HighDateTime, created.LowDateTime), nil
}

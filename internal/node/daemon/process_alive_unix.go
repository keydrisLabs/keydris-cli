//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func ownerProcessMatches(pid int, expected string) (bool, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return false, fmt.Errorf("malformed /proc stat for pid %d", pid)
		}
		fields := strings.Fields(string(data)[end+1:])
		if len(fields) <= 19 {
			return false, fmt.Errorf("malformed /proc stat for pid %d", pid)
		}
		return expected == "linux-start:"+fields[19], nil
	}

	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "comm=").Output()
	if err != nil {
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			signalErr := process.Signal(syscall.Signal(0))
			if errors.Is(signalErr, syscall.ESRCH) {
				return false, nil
			}
			if errors.Is(signalErr, syscall.EPERM) {
				return false, err
			}
		}
		return false, err
	}
	identity := strings.TrimSpace(string(out))
	if identity == "" {
		return false, nil
	}
	return expected == runtime.GOOS+"-start:"+identity, nil
}

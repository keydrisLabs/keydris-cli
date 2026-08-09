//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func stopProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

// processIdentity returns an OS process creation identity, not merely a PID.
// This prevents a stale pidfile from signalling a different process after PID
// reuse.
func processIdentity(pid int) (string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return "", err
		}
		// The command name is parenthesized and may contain spaces. Fields after
		// the final ')' begin at field 3; starttime is field 22 (index 19 here).
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return "", fmt.Errorf("malformed /proc stat for pid %d", pid)
		}
		fields := strings.Fields(string(data)[end+1:])
		if len(fields) <= 19 {
			return "", fmt.Errorf("malformed /proc stat for pid %d", pid)
		}
		return "linux-start:" + fields[19], nil
	}

	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	started := strings.TrimSpace(string(out))
	if started == "" {
		return "", fmt.Errorf("pid %d is not running", pid)
	}
	return runtime.GOOS + "-start:" + started, nil
}

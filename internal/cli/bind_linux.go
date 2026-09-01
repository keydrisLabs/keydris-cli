//go:build linux

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// cgroupRoot is the cgroup v2 mount; keydris sessions live under <root>/keydris.
const cgroupRoot = "/sys/fs/cgroup"

// bindSession creates a cgroup v2 group for the session and moves the calling
// process tree (the parent that invoked the hook, i.e. the agent) into it. It
// returns the cgroup path as it appears in /proc/<pid>/cgroup ("/keydris/<id>"),
// which is the handle the daemon resolver matches on.
func bindSession(sessionID string) (string, error) {
	rel := filepath.Join("/keydris", sessionID)
	dir := filepath.Join(cgroupRoot, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cgroup %s: %w", dir, err)
	}
	// Move the agent (our parent process) into the cgroup so its egress is
	// attributed to this session.
	pid := os.Getppid()
	procs := filepath.Join(dir, "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return "", fmt.Errorf("attach pid %d to cgroup: %w", pid, err)
	}
	return rel, nil
}

// unbindSession removes the session's cgroup (best-effort; it must be empty).
func unbindSession(handle string) error {
	dir := filepath.Join(cgroupRoot, handle)
	return os.Remove(dir)
}

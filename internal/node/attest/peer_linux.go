//go:build linux

package attest

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PeerCheckSupported reports whether connecting-process resolution works on this
// platform. Linux resolves it via /proc; other platforms cannot.
func PeerCheckSupported() bool { return true }

// ConnPID resolves the local pid that owns the socket whose local endpoint is
// srcIP:srcPort — i.e. the process on the other end of a loopback connection the
// proxy accepted. Reuses the same /proc walk as the transparent resolver.
func ConnPID(srcIP string, srcPort int) (int, bool) {
	inode, err := inodeForLocal(srcIP, srcPort)
	if err != nil {
		return 0, false
	}
	pid, err := pidForInode(inode)
	if err != nil {
		return 0, false
	}
	return pid, true
}

// ParentPID reads the parent pid from /proc/<pid>/stat. The comm field (field 2)
// is parenthesized and may contain spaces, so parsing starts after the last ')'.
func ParentPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(data)
	rp := strings.LastIndexByte(s, ')')
	if rp < 0 || rp+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[rp+2:]) // state ppid pgrp ...
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

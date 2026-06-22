//go:build linux

package attest

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procResolver attributes a connection by walking /proc. It is the default,
// dependency-free Linux resolver; it is mildly racy (the process may exit
// between connect and lookup), which the eBPF tracer eliminates.
type procResolver struct{ reg *SessionRegistry }

// NewResolver returns the Linux /proc-based resolver.
func NewResolver(reg *SessionRegistry) Resolver { return &procResolver{reg: reg} }

func (r *procResolver) Resolve(srcIP string, srcPort int) (Attribution, error) {
	inode, err := inodeForLocal(srcIP, srcPort)
	if err != nil {
		return Attribution{}, err
	}
	pid, err := pidForInode(inode)
	if err != nil {
		return Attribution{}, err
	}

	attr := Attribution{PID: pid, Cgroup: cgroupForPID(pid)}
	if r.reg != nil && attr.Cgroup != "" {
		if s, ok := r.reg.Lookup(attr.Cgroup); ok {
			attr.SessionID = s.SPIFFEID
			attr.SVID = s.SVID
		}
	}
	return attr, nil
}

// inodeForLocal finds the socket inode whose local endpoint is srcIP:srcPort.
func inodeForLocal(ip string, port int) (string, error) {
	hexIP := ipToHexLE(ip)
	if hexIP == "" {
		return "", fmt.Errorf("unsupported source IP %q", ip)
	}
	target := fmt.Sprintf("%s:%04X", hexIP, port)
	if ino, ok := scanProcNetTCP("/proc/net/tcp", target); ok {
		return ino, nil
	}
	return "", fmt.Errorf("no local socket for %s:%d", ip, port)
}

func scanProcNetTCP(path, target string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // skip header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if strings.EqualFold(fields[1], target) {
			return fields[9], true // inode column
		}
	}
	return "", false
}

// ipToHexLE renders an IPv4 address as little-endian hex (as in /proc/net/tcp).
func ipToHexLE(ip string) string {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X%02X", p[3], p[2], p[1], p[0])
}

// pidForInode finds the pid owning the socket inode by scanning /proc/*/fd.
func pidForInode(inode string) (int, error) {
	want := "socket:[" + inode + "]"
	procs, _ := filepath.Glob("/proc/[0-9]*")
	for _, p := range procs {
		fds, _ := filepath.Glob(p + "/fd/*")
		for _, fd := range fds {
			if link, err := os.Readlink(fd); err == nil && link == want {
				return strconv.Atoi(filepath.Base(p))
			}
		}
	}
	return 0, fmt.Errorf("no pid owns socket inode %s", inode)
}

// cgroupForPID returns the cgroup v2 path of a pid (the "0::" line).
func cgroupForPID(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::")
		}
	}
	return ""
}

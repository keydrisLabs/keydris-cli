package attest

// ParentFunc returns the parent pid of pid and whether it could be determined.
// It is the seam that makes IsDescendant testable without /proc.
type ParentFunc func(pid int) (ppid int, ok bool)

// IsDescendant reports whether pid is ancestor, or a descendant of ancestor,
// by walking parent links toward the root. The walk is bounded so a corrupt or
// cyclic chain cannot loop forever.
func IsDescendant(pid, ancestor int, parent ParentFunc) bool {
	if pid <= 0 || ancestor <= 0 {
		return false
	}
	for i := 0; i < 64 && pid > 1; i++ {
		if pid == ancestor {
			return true
		}
		ppid, ok := parent(pid)
		if !ok || ppid <= 0 || ppid == pid {
			return false
		}
		pid = ppid
	}
	return pid == ancestor
}

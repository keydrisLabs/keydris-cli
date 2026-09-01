//go:build !linux

package attest

// On non-Linux platforms a userspace loopback proxy cannot reliably resolve the
// connecting process. The robust answer is kernel-asserted attribution
// (NETransparentProxyProvider on macOS, WFP on Windows). Until then peer
// verification is a no-op here.

// PeerCheckSupported reports that connecting-process resolution is unavailable.
func PeerCheckSupported() bool { return false }

// ConnPID cannot resolve the peer pid on this platform.
func ConnPID(string, int) (int, bool) { return 0, false }

// ParentPID cannot resolve a parent pid on this platform.
func ParentPID(int) (int, bool) { return 0, false }

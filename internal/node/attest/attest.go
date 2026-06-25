// Package attest maps an intercepted connection to the originating process and,
// via the session registry, to a registered per-session SPIFFE identity.
//
// On Linux the default resolver reads /proc (source 4-tuple -> socket inode ->
// pid -> cgroup); the eBPF tracer in internal/node/ebpf is the race-free production
// upgrade behind the "ebpf" build tag. The session registry (cgroup handle ->
// session) is populated by the SessionStart hook (Phase 4).
package attest

import (
	"errors"
	"sync"
)

// ErrUnsupported is returned by the resolver on non-Linux platforms.
var ErrUnsupported = errors.New("connection attribution is only supported on Linux")

// Session is a registered per-session identity bound to a platform handle.
type Session struct {
	Handle    string // cgroup path (Linux) or opaque token
	SPIFFEID  string
	SVID      string
	Blueprint string
	// OwnerPID is the session's root process. When non-zero, the sandbox proxy
	// verifies a connecting process is a descendant of it before honoring the
	// session's token (peer verification). Zero means "unknown — skip the check".
	OwnerPID int
}

// Attribution is the resolved origin of a connection.
type Attribution struct {
	PID       int
	Cgroup    string
	SessionID string // SPIFFE ID if a session is registered for the handle, else ""
	SVID      string // the session's SVID if registered
}

// SessionRegistry maps a platform handle (cgroup path) to a registered session.
type SessionRegistry struct {
	mu sync.RWMutex
	m  map[string]Session
}

// NewSessionRegistry returns an empty registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{m: map[string]Session{}}
}

// Register binds a session to its handle.
func (r *SessionRegistry) Register(s Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[s.Handle] = s
}

// Unregister removes a session by handle.
func (r *SessionRegistry) Unregister(handle string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, handle)
}

// SetOwnerPID updates the owner pid of an already-registered session (used for
// peer verification). It returns false if no session is registered for handle.
func (r *SessionRegistry) SetOwnerPID(handle string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[handle]
	if !ok {
		return false
	}
	s.OwnerPID = pid
	r.m[handle] = s
	return true
}

// Lookup returns the session registered for a handle.
func (r *SessionRegistry) Lookup(handle string) (Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.m[handle]
	return s, ok
}

// Sole returns the only registered session when exactly one exists. The sandbox
// proxy uses this as the attribution fallback for the common case of one Claude
// session per proxy instance, when no per-session handle is carried on the wire.
func (r *SessionRegistry) Sole() (Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.m) != 1 {
		return Session{}, false
	}
	for _, s := range r.m {
		return s, true
	}
	return Session{}, false
}

// Resolver resolves a connection's source endpoint to its origin process and
// (if registered) session.
type Resolver interface {
	Resolve(srcIP string, srcPort int) (Attribution, error)
}

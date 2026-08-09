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
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// ErrUnsupported is returned by the resolver on non-Linux platforms.
var ErrUnsupported = errors.New("connection attribution is only supported on Linux")

// Session is a registered per-session identity bound to a platform handle.
type Session struct {
	Handle    string // cgroup path (Linux) or opaque token
	SPIFFEID  string
	SVID      string
	Blueprint string
	AgentID   string
	ULID      string
	ExpiresAt string
	SessionID string
	IssuedAt  time.Time // local registration/renewal time used to schedule refresh
	Routes    *runtimecontract.RuntimeRoutes
	// OwnerPID is the session's root process. When non-zero, the sandbox proxy
	// verifies a connecting process is a descendant of it before honoring the
	// session's token (peer verification). Zero means "unknown — skip the check".
	OwnerPID int
	// OwnerManaged is true only when Keydris launched OwnerPID itself and can
	// therefore use process exit as an authoritative session-end signal. Hook
	// parents (notably Claude's command-hook shell) are deliberately not marked
	// managed because that intermediate process may exit while the session lives.
	OwnerManaged bool
	// OwnerIdentity is the OS process-creation identity paired with OwnerPID. It
	// prevents PID reuse from making an exited managed session look alive again.
	OwnerIdentity string
}

// Attribution is the resolved origin of a connection.
type Attribution struct {
	PID       int
	Cgroup    string
	SessionID string // SPIFFE ID if a session is registered for the handle, else ""
	SVID      string // the session's SVID if registered
	Routes    *runtimecontract.RuntimeRoutes
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

// Take atomically removes and returns a session. SessionEnd uses the returned
// current ULID so it can revoke a credential that may have been renewed since
// the original hook state was written.
func (r *SessionRegistry) Take(handle string) (Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.m[handle]
	if ok {
		delete(r.m, handle)
	}
	return session, ok
}

// SetOwner updates the owner process of an already-registered session. The
// managed bit distinguishes a process Keydris launched from an advisory hook
// parent used only for peer verification.
func (r *SessionRegistry) SetOwner(handle string, pid int, managed bool, identity string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[handle]
	if !ok {
		return false
	}
	s.OwnerPID = pid
	s.OwnerManaged = managed
	s.OwnerIdentity = identity
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

// Snapshot returns a stable copy for background renewal without holding the
// registry lock during network calls.
func (r *SessionRegistry) Snapshot() []Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Session, 0, len(r.m))
	for _, session := range r.m {
		out = append(out, session)
	}
	return out
}

// Replace swaps a session only if the handle still points at the expected ULID.
func (r *SessionRegistry) Replace(handle, expectedULID string, next Session) bool {
	replaced, _ := r.ReplaceWith(handle, expectedULID, next, nil)
	return replaced
}

// ReplaceWith atomically verifies and swaps a renewed session. persist runs
// while the registry lock is held, preventing SessionEnd from unregistering the
// renewed identity before its durable state has been replaced.
func (r *SessionRegistry) ReplaceWith(
	handle,
	expectedULID string,
	next Session,
	persist func() error,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.m[handle]
	if !ok || current.ULID != expectedULID {
		return false, nil
	}
	r.m[handle] = next
	if persist != nil {
		return true, persist()
	}
	return true, nil
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

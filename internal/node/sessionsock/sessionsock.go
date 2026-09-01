// Package sessionsock is the daemon's local session-registration channel: a
// unix-domain socket the keydris CLI hooks use to bind/unbind a per-session
// SPIFFE identity to a platform handle (cgroup path). The daemon's attribution
// resolver then maps intercepted connections (handle -> session) to the SVID.
//
// It is a local trust boundary: only processes that can reach the socket may
// register sessions, so the daemon should own it with restricted perms in a
// real deployment.
package sessionsock

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// Actions.
const (
	ActionRegister    = "register"
	ActionUnregister  = "unregister"
	ActionUpdateOwner = "update-owner" // set OwnerPID on an existing session
	ActionLookup      = "lookup"
)

// Message is one line-delimited JSON request on the socket.
type Message struct {
	Auth      string `json:"auth"`
	Action    string `json:"action"`
	Handle    string `json:"handle"` // cgroup path
	SPIFFEID  string `json:"spiffe_id,omitempty"`
	SVID      string `json:"svid,omitempty"`
	Blueprint string `json:"blueprint,omitempty"`
	ULID      string `json:"ulid,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	OwnerPID  int    `json:"owner_pid,omitempty"` // session root pid, for peer verification
	// OwnerManaged is set only by `keydris run`, which launched OwnerPID and can
	// treat its exit as authoritative. Claude hook parent PIDs are not reliable
	// lifecycle anchors because the hook may be invoked through a short-lived shell.
	OwnerManaged  bool                           `json:"owner_managed,omitempty"`
	OwnerIdentity string                         `json:"owner_identity,omitempty"`
	Routes        *runtimecontract.RuntimeRoutes `json:"routes,omitempty"`
}

// SessionSnapshot is returned only over the authenticated, owner-only local
// socket. It lets short-lived hooks use and revoke the daemon's current
// credential after background renewal.
type SessionSnapshot struct {
	Handle        string `json:"handle"`
	SPIFFEID      string `json:"spiffe_id"`
	SVID          string `json:"svid"`
	Blueprint     string `json:"blueprint"`
	ULID          string `json:"ulid"`
	AgentID       string `json:"agent_id"`
	ExpiresAt     string `json:"expires_at"`
	SessionID     string `json:"session_id"`
	OwnerPID      int    `json:"owner_pid,omitempty"`
	OwnerManaged  bool   `json:"owner_managed,omitempty"`
	OwnerIdentity string `json:"owner_identity,omitempty"`
}

type response struct {
	OK      bool             `json:"ok"`
	Error   string           `json:"error,omitempty"`
	Session *SessionSnapshot `json:"session,omitempty"`
}

// Server accepts registration messages and applies them to a SessionRegistry.
type Server struct {
	ln     net.Listener
	reg    *attest.SessionRegistry
	secret string
	logf   func(string, ...any)
}

// Serve starts a registration server on the unix socket at path. The socket is
// owned by the data-directory user and every message must authenticate.
func Serve(path, secret string, reg *attest.SessionRegistry, logf func(string, ...any)) (*Server, error) {
	if secret == "" {
		return nil, fmt.Errorf("empty session socket secret")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	_ = os.Remove(path) // clear a stale socket
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := secureSocket(path); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}

	s := &Server{ln: ln, reg: reg, secret: secret, logf: logf}
	go s.accept()
	return s, nil
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), (3<<20)+8192)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			fmt.Fprintln(conn, `{"ok":false,"error":"bad message"}`)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(m.Auth), []byte(s.secret)) != 1 {
			fmt.Fprintln(conn, `{"ok":false,"error":"unauthorized"}`)
			continue
		}
		if err := validateMessage(m); err != nil {
			fmt.Fprintln(conn, `{"ok":false,"error":"invalid message"}`)
			continue
		}
		var snapshot *SessionSnapshot
		switch m.Action {
		case ActionRegister:
			s.reg.Register(attest.Session{
				Handle: m.Handle, SPIFFEID: m.SPIFFEID, SVID: m.SVID,
				Blueprint: m.Blueprint, AgentID: m.AgentID, ULID: m.ULID,
				ExpiresAt: m.ExpiresAt, SessionID: m.SessionID, OwnerPID: m.OwnerPID,
				OwnerManaged: m.OwnerManaged, OwnerIdentity: m.OwnerIdentity,
				IssuedAt: time.Now().UTC(),
				Routes:   m.Routes,
			})
			// The handle is a per-session bearer token; log only a short prefix.
			s.logf("session registered: handle=%s spiffe=%s owner_pid=%d", handlePrefix(m.Handle), m.SPIFFEID, m.OwnerPID)
		case ActionUpdateOwner:
			if s.reg.SetOwner(m.Handle, m.OwnerPID, m.OwnerManaged, m.OwnerIdentity) {
				s.logf("session owner set: handle=%s owner_pid=%d", handlePrefix(m.Handle), m.OwnerPID)
			}
		case ActionUnregister:
			if current, ok := s.reg.Take(m.Handle); ok {
				value := snapshotSession(current)
				snapshot = &value
			}
			s.logf("session unregistered: handle=%s", handlePrefix(m.Handle))
		case ActionLookup:
			current, ok := s.reg.Lookup(m.Handle)
			if !ok {
				fmt.Fprintln(conn, `{"ok":false,"error":"session not found"}`)
				continue
			}
			value := snapshotSession(current)
			snapshot = &value
		default:
			fmt.Fprintln(conn, `{"ok":false,"error":"unknown action"}`)
			continue
		}
		if err := json.NewEncoder(conn).Encode(response{OK: true, Session: snapshot}); err != nil {
			return
		}
	}
}

func snapshotSession(session attest.Session) SessionSnapshot {
	return SessionSnapshot{
		Handle: session.Handle, SPIFFEID: session.SPIFFEID, SVID: session.SVID,
		Blueprint: session.Blueprint, ULID: session.ULID, AgentID: session.AgentID,
		ExpiresAt: session.ExpiresAt, SessionID: session.SessionID,
		OwnerPID: session.OwnerPID, OwnerManaged: session.OwnerManaged,
		OwnerIdentity: session.OwnerIdentity,
	}
}

func validateMessage(m Message) error {
	if m.Handle == "" || len(m.Handle) > 4096 {
		return fmt.Errorf("invalid handle")
	}
	switch m.Action {
	case ActionRegister:
		if m.SPIFFEID == "" || m.SVID == "" ||
			len(m.SPIFFEID) > 2048 || len(m.SVID) > 1<<20 {
			return fmt.Errorf("invalid registration")
		}
		if m.Routes == nil || m.Routes.Validate() != nil {
			return fmt.Errorf("invalid runtime routes")
		}
	case ActionUpdateOwner:
		if m.OwnerPID <= 0 || len(m.OwnerIdentity) > 512 {
			return fmt.Errorf("invalid owner pid")
		}
	case ActionUnregister, ActionLookup:
	default:
		return fmt.Errorf("unknown action")
	}
	return nil
}

// handlePrefix returns a short, non-secret prefix of a session handle for logs.
// In the sandbox plane the handle is a per-session bearer token, so the full
// value is never written to logs.
func handlePrefix(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12] + "…"
}

// Close stops the server and removes the socket.
func (s *Server) Close() error {
	if s.ln == nil {
		return nil
	}
	err := s.ln.Close()
	_ = os.Remove(s.ln.Addr().String())
	return err
}

// Send delivers a single message to the daemon socket and waits for the ack.
func Send(path string, m Message) error {
	_, err := Exchange(path, m)
	return err
}

// Exchange sends a message and returns the current session snapshot for lookup
// and unregister actions.
func Exchange(path string, m Message) (*SessionSnapshot, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial daemon socket %s: %w", path, err)
	}
	defer conn.Close()

	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		return nil, fmt.Errorf("no ack from daemon socket")
	}
	var ack response
	if err := json.Unmarshal(sc.Bytes(), &ack); err != nil {
		return nil, fmt.Errorf("bad ack: %w", err)
	}
	if !ack.OK {
		return nil, fmt.Errorf("daemon rejected: %s", ack.Error)
	}
	return ack.Session, nil
}

// LoadOrCreateSecret returns the per-install authentication secret used by the
// local session socket. It is created owner-only and is safe under concurrent
// CLI/daemon startup.
func LoadOrCreateSecret(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty session auth path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)

	emptyReads := 0
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			var raw [32]byte
			if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return "", err
			}
			secret := hex.EncodeToString(raw[:])
			if _, err := io.WriteString(f, secret+"\n"); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return "", err
			}
			if err := f.Sync(); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return "", err
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return "", err
			}
			return secret, nil
		}
		if !os.IsExist(err) {
			return "", err
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			// Another process may have won O_EXCL and not finished its tiny
			// write yet. Retry briefly rather than treating startup concurrency
			// as permanent secret corruption.
			emptyReads++
			if emptyReads >= 20 {
				return "", fmt.Errorf("empty session auth secret %s", path)
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		decoded, decodeErr := hex.DecodeString(secret)
		if decodeErr != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid session auth secret %s", path)
		}
		_ = os.Chmod(path, 0o600)
		return secret, nil
	}
}

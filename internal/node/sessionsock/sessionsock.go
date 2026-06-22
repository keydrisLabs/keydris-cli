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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nocaplabs/keydris-cli/internal/node/attest"
)

// Actions.
const (
	ActionRegister   = "register"
	ActionUnregister = "unregister"
)

// Message is one line-delimited JSON request on the socket.
type Message struct {
	Action    string `json:"action"`
	Handle    string `json:"handle"` // cgroup path
	SPIFFEID  string `json:"spiffe_id,omitempty"`
	SVID      string `json:"svid,omitempty"`
	Blueprint string `json:"blueprint,omitempty"`
	ULID      string `json:"ulid,omitempty"`
}

// Server accepts registration messages and applies them to a SessionRegistry.
type Server struct {
	ln   net.Listener
	reg  *attest.SessionRegistry
	logf func(string, ...any)
}

// Serve starts a registration server on the unix socket at path. The socket is
// created world-writable for the POC (root daemon, non-root hook caller).
func Serve(path string, reg *attest.SessionRegistry, logf func(string, ...any)) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path) // clear a stale socket
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o666)

	s := &Server{ln: ln, reg: reg, logf: logf}
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
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			fmt.Fprintln(conn, `{"ok":false,"error":"bad message"}`)
			continue
		}
		switch m.Action {
		case ActionRegister:
			s.reg.Register(attest.Session{Handle: m.Handle, SPIFFEID: m.SPIFFEID, SVID: m.SVID, Blueprint: m.Blueprint})
			s.logf("session registered: handle=%q spiffe=%s", m.Handle, m.SPIFFEID)
		case ActionUnregister:
			s.reg.Unregister(m.Handle)
			s.logf("session unregistered: handle=%q", m.Handle)
		default:
			fmt.Fprintln(conn, `{"ok":false,"error":"unknown action"}`)
			continue
		}
		fmt.Fprintln(conn, `{"ok":true}`)
	}
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
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("dial daemon socket %s: %w", path, err)
	}
	defer conn.Close()

	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return err
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		return fmt.Errorf("no ack from daemon socket")
	}
	var ack struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &ack); err != nil {
		return fmt.Errorf("bad ack: %w", err)
	}
	if !ack.OK {
		return fmt.Errorf("daemon rejected: %s", ack.Error)
	}
	return nil
}

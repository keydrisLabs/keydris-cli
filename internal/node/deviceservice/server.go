package deviceservice

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	actionHealth    = "health"
	actionIdentity  = "identity"
	actionCreateCSR = "create-csr"
	actionSign      = "sign"
	actionInstall   = "install-identity"
	actionRenew     = "renew"
)

type request struct {
	Version  int          `json:"version"`
	Action   string       `json:"action"`
	Sign     *SignRequest `json:"sign,omitempty"`
	Identity *Identity    `json:"identity,omitempty"`
	CSR      *CSRRequest  `json:"csr,omitempty"`
}

type response struct {
	OK        bool       `json:"ok"`
	Error     string     `json:"error,omitempty"`
	Health    *Health    `json:"health,omitempty"`
	Identity  *Identity  `json:"identity,omitempty"`
	Signature *Signature `json:"signature,omitempty"`
	CSR       *CSR       `json:"csr,omitempty"`
}

type PeerResolver func(net.Conn) (Caller, error)

type ServerOptions struct {
	ExpectedUID uint32
	ResolvePeer PeerResolver
}

type Server struct {
	listener net.Listener
	service  *Service
	resolve  PeerResolver
	close    sync.Once
	slots    chan struct{}
}

// Serve starts the root device service. The socket is intentionally reachable
// by local users; authorization derives the caller UID from the kernel rather
// than trusting request fields or a shared bearer secret.
func Serve(path string, service *Service, options ServerOptions) (*Server, error) {
	if service == nil {
		return nil, fmt.Errorf("device service is required")
	}
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("device service socket must be absolute")
	}
	if options.ResolvePeer == nil {
		options.ResolvePeer = peerCredentials
	}
	if err := prepareSocketPath(path, options.ExpectedUID); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on device service socket: %w", err)
	}
	if err := secureServiceSocket(path, options.ExpectedUID); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	server := &Server{listener: listener, service: service, resolve: options.ResolvePeer, slots: make(chan struct{}, 64)}
	go server.accept()
	return server, nil
}

func prepareSocketPath(path string, expectedUID uint32) error {
	parent := filepath.Dir(path)
	if _, err := os.Lstat(parent); err == nil {
		// Validate before chmod so an attacker-controlled link is never followed.
		if err := validateSocketDirectory(parent, expectedUID); err != nil {
			return err
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create device service socket directory: %w", err)
		}
		if err := os.Chmod(parent, 0o755); err != nil {
			return err
		}
		if err := validateSocketDirectory(parent, expectedUID); err != nil {
			return err
		}
	} else {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace non-socket device service path %s", path)
	}
	if err := validateSocketOwner(info, expectedUID); err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *Server) accept() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		select {
		case s.slots <- struct{}{}:
			go func() {
				defer func() { <-s.slots }()
				s.handle(connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (s *Server) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	caller, err := s.resolve(connection)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(response{Error: "cannot verify local caller"})
		return
	}
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	if !scanner.Scan() {
		return
	}
	var message request
	if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
		_ = json.NewEncoder(connection).Encode(response{Error: "bad request"})
		return
	}
	reply := s.dispatch(context.Background(), caller, message)
	_ = json.NewEncoder(connection).Encode(reply)
}

func (s *Server) dispatch(ctx context.Context, caller Caller, message request) response {
	if message.Version != ProtocolVersion {
		return response{Error: "unsupported protocol version"}
	}
	switch message.Action {
	case actionHealth:
		value := s.service.Health(caller)
		return response{OK: true, Health: &value}
	case actionIdentity:
		value, err := s.service.Identity()
		return identityResponse(value, err)
	case actionCreateCSR:
		if message.CSR == nil {
			return response{Error: "missing CSR request"}
		}
		value, err := s.service.CreateCSR(caller, *message.CSR)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, CSR: &value}
	case actionSign:
		if message.Sign == nil {
			return response{Error: "missing sign request"}
		}
		value, err := s.service.Sign(caller, *message.Sign)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Signature: &value}
	case actionInstall:
		if message.Identity == nil {
			return response{Error: "missing device identity"}
		}
		if err := s.service.InstallIdentity(caller, *message.Identity); err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true}
	case actionRenew:
		value, err := s.service.Renew(ctx, caller)
		return identityResponse(value, err)
	default:
		return response{Error: "unknown action"}
	}
}

func identityResponse(value *Identity, err error) response {
	if err != nil {
		return response{Error: err.Error()}
	}
	return response{OK: true, Identity: value}
}

func (s *Server) Close() error {
	var err error
	s.close.Do(func() {
		path := s.listener.Addr().String()
		err = s.listener.Close()
		_ = os.Remove(path)
	})
	return err
}

// Package desktopproxy is a loopback HTTP forwarder for Claude Desktop.
//
// The Desktop app and Cowork VM cannot put a session handle in the proxy URL.
// This process accepts their CONNECT (and absolute-form HTTP) requests, stamps
// Proxy-Authorization, and dials the Keydris proxy. It does not terminate TLS,
// read bodies for inspection, or make policy decisions.
package desktopproxy

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Server is a loopback forwarder that stamps a fixed session handle onto every
// request before dialing the Keydris proxy.
type Server struct {
	ln           net.Listener
	upstreamAddr string
	proxyAuth    string
}

// Listen binds 127.0.0.1:0. upstreamAddr is the Keydris proxy, typically
// 127.0.0.1:15001. handle is the session handle sent as the Proxy-Authorization
// password. The username is "keydris" (the proxy ignores the username). handle
// must be non-empty.
func Listen(upstreamAddr, handle string) (*Server, error) {
	if handle == "" {
		return nil, fmt.Errorf("desktopproxy: empty handle")
	}
	if upstreamAddr == "" {
		return nil, fmt.Errorf("desktopproxy: empty upstream address")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln:           ln,
		upstreamAddr: upstreamAddr,
		proxyAuth:    "Basic " + base64.StdEncoding.EncodeToString([]byte("keydris:"+handle)),
	}
	go s.acceptLoop()
	return s, nil
}

// Addr returns the forwarder's listen address as host:port.
func (s *Server) Addr() string {
	return s.ln.Addr().String()
}

// Close closes the listener and stops accepting new connections.
func (s *Server) Close() error {
	return s.ln.Close()
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(client net.Conn) {
	defer client.Close()
	if !clientAllowed(client.RemoteAddr()) {
		return
	}

	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	req.Header.Set("Proxy-Authorization", s.proxyAuth)

	upstream, err := net.Dial("tcp", s.upstreamAddr)
	if err != nil {
		return
	}
	defer upstream.Close()

	if err := req.Write(upstream); err != nil {
		return
	}
	splice(client, upstream, br)
}

// clientAllowed reports whether addr is a loopback client (127.0.0.1 or ::1).
func clientAllowed(addr net.Addr) bool {
	ta, ok := addr.(*net.TCPAddr)
	return ok && ta.IP != nil && ta.IP.IsLoopback()
}

func splice(client, upstream net.Conn, clientReader io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
	<-done
}

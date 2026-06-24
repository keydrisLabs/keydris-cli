package dataplane

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/nocaplabs/keydris-cli/internal/node/attest"
	"github.com/nocaplabs/keydris-cli/internal/node/proxy"
)

// sandboxPlane is the v2 data plane: a TLS-terminating forward proxy that Claude
// Code's sandbox routes Bash-subprocess egress to (sandbox.network.httpProxyPort).
//
// Unlike the transparent plane it touches no kernel state. The sandbox already
// guarantees that the agent and its subprocesses cannot reach the network except
// through this proxy, so attribution collapses to "read the per-session handle,
// look it up in the registry":
//
//   - https:// upstreams arrive as CONNECT; the plane answers 200, terminates
//     TLS with a leaf signed by the Keydris CA (trusted inside the sandbox),
//     reads the decrypted request, and re-originates TLS to the real upstream
//     after the broker injects the credential.
//   - http:// upstreams arrive as absolute-form proxy requests, handled like the
//     proxy-env fallback.
//
// Per-session attribution (which Claude session this egress belongs to) is read
// from the CONNECT request's Proxy-Authorization handle when present; otherwise,
// when exactly one session is registered, that session is used (the common POC
// case of one Claude session per proxy instance). See docs for the per-session
// port option when multiple concurrent sessions must be separated.
type sandboxPlane struct {
	ln    net.Listener
	flows chan Flow
	logf  func(string, ...any)
	ca    *proxy.CA
	reg   *attest.SessionRegistry

	leafMu sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewSandboxProxy starts the sandbox custom-proxy plane on addr. ca terminates
// TLS for intercepted HTTPS; reg resolves the per-session handle to its SVID.
func NewSandboxProxy(addr string, ca *proxy.CA, reg *attest.SessionRegistry) (DataPlane, error) {
	if ca == nil {
		return nil, fmt.Errorf("sandbox proxy requires a CA for TLS termination")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &sandboxPlane{
		ln:     ln,
		flows:  make(chan Flow),
		logf:   log.Printf,
		ca:     ca,
		reg:    reg,
		leaves: map[string]*tls.Certificate{},
	}
	p.logf("dataplane(sandbox): listening on %s (set Claude Code sandbox.network.httpProxyPort=%s)", addr, portOf(addr))
	go p.serve()
	return p, nil
}

func (p *sandboxPlane) Flows() <-chan Flow { return p.flows }

func (p *sandboxPlane) Close() error {
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

func (p *sandboxPlane) serve() {
	defer close(p.flows)
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		// Building a flow may block on the TLS handshake, so do it per-conn.
		go func(c net.Conn) {
			f, ok := p.build(c)
			if !ok {
				return
			}
			p.flows <- f
		}(conn)
	}
}

// build reads the first request from conn and returns a Flow. For CONNECT it
// performs the TLS MITM and reads the decrypted request; for absolute-form it
// behaves like the proxy-env plane. ok is false when the connection was already
// handled/closed (error, or no destination).
func (p *sandboxPlane) build(conn net.Conn) (Flow, bool) {
	req, br, err := readRequest(conn)
	if err != nil {
		_ = conn.Close()
		return Flow{}, false
	}

	if req.Method == http.MethodConnect {
		return p.buildConnect(conn, req)
	}
	return p.buildPlain(conn, req, br)
}

func (p *sandboxPlane) buildConnect(conn net.Conn, connectReq *http.Request) (Flow, bool) {
	target := connectReq.Host // host:port
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}

	sess := p.resolveSession(connectReq)

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = conn.Close()
		return Flow{}, false
	}

	// Mint the leaf for the CONNECT target host. Clients dialing an IP literal
	// send no SNI, so we cannot rely on ClientHelloInfo.ServerName alone; fall
	// back to the target host the CONNECT line gave us.
	tlsConf := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return p.leafFor(name)
		},
	}
	tconn := tls.Server(conn, tlsConf)
	if err := tconn.Handshake(); err != nil {
		p.logf("dataplane(sandbox): TLS handshake for %s: %v", target, err)
		_ = tconn.Close()
		return Flow{}, false
	}

	req, br, err := readRequest(tconn)
	if err != nil {
		_ = tconn.Close()
		return Flow{}, false
	}

	f := Flow{
		dst:         target,
		dstHost:     host,
		upstreamTLS: true,
		conn:        tconn,
		req:         req,
		br:          br,
	}
	if ap, err := netip.ParseAddrPort(target); err == nil {
		f.OrigDst = ap
	}
	applySession(&f, sess)
	return f, true
}

// buildPlain handles absolute-form proxy requests for http:// upstreams, the
// same shape the proxy-env plane reads.
func (p *sandboxPlane) buildPlain(conn net.Conn, req *http.Request, br *bufio.Reader) (Flow, bool) {
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	if host == "" {
		_ = conn.Close()
		return Flow{}, false
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "80")
	}

	f := Flow{dst: host, conn: conn, req: req, br: br}
	if ap, err := netip.ParseAddrPort(host); err == nil {
		f.OrigDst = ap
	}
	applySession(&f, p.resolveSession(req))
	return f, true
}

// leafFor returns a cached leaf certificate for host, minting one via the CA on
// first use.
func (p *sandboxPlane) leafFor(host string) (*tls.Certificate, error) {
	p.leafMu.Lock()
	defer p.leafMu.Unlock()
	if cert, ok := p.leaves[host]; ok {
		return cert, nil
	}
	leaf, err := p.ca.LeafFor(host)
	if err != nil {
		return nil, err
	}
	p.leaves[host] = &leaf
	return &leaf, nil
}

// resolveSession attributes a connection to its session. A per-session token in
// Proxy-Authorization is honored exactly: a present-but-unknown token resolves
// to nil (unattributed) rather than guessing — this is what keeps concurrent
// sessions isolated. Only when no token is presented do we fall back to the sole
// registered session (the single-session convenience case).
func (p *sandboxPlane) resolveSession(req *http.Request) *attest.Session {
	if p.reg == nil {
		return nil
	}
	if handle := handleFromProxyAuth(req.Header.Get("Proxy-Authorization")); handle != "" {
		if s, ok := p.reg.Lookup(handle); ok {
			return &s
		}
		return nil // token presented but unknown: do not fall back to Sole()
	}
	if s, ok := p.reg.Sole(); ok {
		return &s
	}
	return nil
}

func applySession(f *Flow, s *attest.Session) {
	if s == nil {
		return
	}
	f.SessionID = s.SPIFFEID
	f.SVID = s.SVID
}

// handleFromProxyAuth extracts the Keydris session handle from a
// "Basic base64(user:handle)" Proxy-Authorization value. The username is
// ignored; the password carries the handle.
func handleFromProxyAuth(h string) string {
	const prefix = "Basic "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return ""
	}
	_, handle, ok := strings.Cut(string(raw), ":")
	if !ok {
		return ""
	}
	return handle
}

func (p *sandboxPlane) Inject(f Flow, c Credential) error {
	defer f.conn.Close()
	if f.req == nil || f.br == nil {
		return errNoRequest
	}
	if f.upstreamTLS {
		return injectAndForwardTLS(f.conn, f.br, f.req, f.DstString(), f.dstHost, c)
	}
	return injectAndForward(f.conn, f.br, f.req, f.DstString(), c)
}

func (p *sandboxPlane) Reject(f Flow, reason string) error {
	defer f.conn.Close()
	writeReject(f.conn, reason)
	return nil
}

func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return addr
}

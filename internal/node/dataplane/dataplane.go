// Package dataplane is the OS-specific seam described in plan.md section 7.
//
// All platform-specific interception and attribution lives behind the DataPlane
// interface; everything downstream (broker authorize, credential injection) is
// shared. Phase 1 ships the Linux transparent plane plus a cross-platform
// proxy-env fallback. macOS (NETransparentProxyProvider) and Windows (WinDivert)
// planes are post-POC implementations of the same interface.
package dataplane

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
)

// Credential is what the broker tells the data plane to inject. Phase 1 only
// supports Type == "header".
type Credential struct {
	Type  string
	Name  string
	Value string
}

// Flow is one intercepted outbound request. OrigDst/SrcPID/SessionID are the
// portable, platform-independent view; the unexported fields hold the live
// connection state the inline proxies need to inject and splice.
type Flow struct {
	OrigDst       netip.AddrPort  // recovered original destination (best-effort for hostnames)
	SrcPID        int             // originating process (0 if unknown)
	Cgroup        string          // originating cgroup path (Linux; "" if unknown)
	SessionID     string          // SPIFFE ID resolved by attest from the platform handle ("" if none)
	SVID          string          // the session's JWT-SVID, presented to the broker ("" if none)
	ToolCall      string          // intercepted HTTP operation, formatted as "METHOD /path"
	ToolParams    json.RawMessage // intercepted JSON request body (nil when absent/non-JSON)
	MetadataError string          // local request-metadata validation failure; reject before broker

	conn net.Conn
	req  *http.Request
	br   *bufio.Reader
	dst  string // authoritative dial target (host:port), may be a hostname

	upstreamTLS bool   // dial the upstream over TLS (sandbox CONNECT/MITM path)
	dstHost     string // SNI/host for the upstream TLS dial
}

// DstString returns the dial/authorize target for the flow.
func (f Flow) DstString() string {
	if f.dst != "" {
		return f.dst
	}
	return f.OrigDst.String()
}

// DstHost returns the hostname used for authorization and upstream TLS.
func (f Flow) DstHost() string {
	if f.dstHost != "" {
		return f.dstHost
	}
	host, _, err := net.SplitHostPort(f.DstString())
	if err == nil {
		return host
	}
	return f.DstString()
}

// DataPlane abstracts per-OS interception + attribution.
//
// Inject and Reject consume the flow's connection (allow vs deny). Reject is a
// deliberate extension to the plan.md sketch so the enforcement path (Phase 3)
// can return a synthetic 403 without dialing upstream.
type DataPlane interface {
	Flows() <-chan Flow
	Inject(f Flow, c Credential) error
	PassThrough(f Flow) error
	Reject(f Flow, reason string) error
	Close() error
}

var (
	errNoRequest   = errors.New("flow has no buffered request")
	errFlowHandled = errors.New("flow already handled")
)

// inlinePlane is the shared base for inline (proxying) data planes. Concrete
// planes differ only in how they build a Flow from an accepted connection.
type inlinePlane struct {
	ln    net.Listener
	flows chan Flow
	logf  func(string, ...any)
}

func newInlinePlane(ln net.Listener) *inlinePlane {
	return &inlinePlane{
		ln:    ln,
		flows: make(chan Flow),
		logf:  log.Printf,
	}
}

// serve accepts connections, builds a Flow via build, and emits it. The flows
// channel is closed when the listener closes so consumers can range over it.
func (p *inlinePlane) serve(build func(net.Conn) (Flow, error)) {
	var workers sync.WaitGroup
	defer func() {
		workers.Wait()
		close(p.flows)
	}()
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			f, err := build(conn)
			if err != nil {
				if !errors.Is(err, errFlowHandled) {
					p.logf("dataplane: flow build error: %v", err)
				}
				_ = conn.Close()
				return
			}
			p.flows <- f
		}()
	}
}

func (p *inlinePlane) Flows() <-chan Flow { return p.flows }

func (p *inlinePlane) Inject(f Flow, c Credential) error {
	defer f.conn.Close()
	if f.req == nil || f.br == nil {
		return errNoRequest
	}
	return injectAndForward(f.conn, f.br, f.req, f.DstString(), c)
}

func (p *inlinePlane) PassThrough(f Flow) error {
	defer f.conn.Close()
	if f.req == nil || f.br == nil {
		return errNoRequest
	}
	return forwardUnchanged(f.conn, f.br, f.req, f.DstString())
}

func (p *inlinePlane) Reject(f Flow, reason string) error {
	defer f.conn.Close()
	writeReject(f.conn, reason)
	return nil
}

func (p *inlinePlane) Close() error {
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

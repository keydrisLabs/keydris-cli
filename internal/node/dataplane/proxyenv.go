package dataplane

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

// NewProxyEnv starts the cross-platform proxy-env fallback (plan.md section 7,
// Pattern 2). The agent is pointed at this listener via HTTP_PROXY/HTTPS_PROXY;
// the data plane reads the absolute-form request, derives the destination from
// the URL, and injects the credential.
//
// This is the portable, kernel-free path that works on macOS and Windows. It is
// a deliberate downgrade: it is bypassable (the agent could unset the env var)
// and it has no per-connection PID attribution. Plain HTTP can be governed;
// unmanaged HTTPS CONNECT traffic is tunneled opaquely.
func NewProxyEnv(addr string, scope *proxyscope.Scope) (DataPlane, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := newInlinePlane(ln)
	p.logf("dataplane(proxyenv): listening on %s (set HTTP_PROXY=http://%s)", addr, addr)
	go p.serve(func(conn net.Conn) (Flow, error) {
		return buildProxyEnvFlow(conn, scope)
	})
	return p, nil
}

func buildProxyEnvFlow(conn net.Conn, scope *proxyscope.Scope) (Flow, error) {
	req, br, err := readRequest(conn)
	if err != nil {
		return Flow{}, err
	}

	if req.Method == "CONNECT" {
		target, normalizeErr := proxyscope.Normalize(req.Host)
		if normalizeErr != nil {
			writeReject(conn, "invalid CONNECT destination")
			return Flow{}, normalizeErr
		}
		if scope != nil && !scope.Managed(target) {
			passthroughErr := tunnelCONNECT(conn, br, target)
			if passthroughErr != nil {
				return Flow{}, passthroughErr
			}
			return Flow{}, errFlowHandled
		}
		writeReject(conn, "HTTPS CONNECT not supported in the proxy-env fallback (POC)")
		return Flow{}, fmt.Errorf("CONNECT not supported")
	}

	host, err := requestDestination(req, "http")
	if err != nil {
		return Flow{}, err
	}

	f := Flow{
		dst:     host,
		dstHost: hostOnly(host),
		conn:    conn,
		req:     req,
		br:      br,
	}
	if scope == nil || scope.Managed(host) {
		if err := applyRequestMetadata(&f, req); err != nil {
			f.MetadataError = err.Error()
		}
	}
	if ap, err := netip.ParseAddrPort(host); err == nil {
		f.OrigDst = ap
	}
	return f, nil
}

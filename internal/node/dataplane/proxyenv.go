package dataplane

import (
	"fmt"
	"net"
	"net/netip"
)

// NewProxyEnv starts the cross-platform proxy-env fallback (plan.md section 7,
// Pattern 2). The agent is pointed at this listener via HTTP_PROXY/HTTPS_PROXY;
// the data plane reads the absolute-form request, derives the destination from
// the URL, and injects the credential.
//
// This is the portable, kernel-free path that works on macOS and Windows. It is
// a deliberate downgrade: it is bypassable (the agent could unset the env var)
// and it has no per-connection PID attribution. Phase 1 supports plaintext HTTP
// only; HTTPS CONNECT tunneling is a TODO (needs MITM, the plan.md stretch).
func NewProxyEnv(addr string) (DataPlane, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := newInlinePlane(ln)
	p.logf("dataplane(proxyenv): listening on %s (set HTTP_PROXY=http://%s)", addr, addr)
	go p.serve(buildProxyEnvFlow)
	return p, nil
}

func buildProxyEnvFlow(conn net.Conn) (Flow, error) {
	req, br, err := readRequest(conn)
	if err != nil {
		return Flow{}, err
	}

	if req.Method == "CONNECT" {
		writeReject(conn, "HTTPS CONNECT not supported in the proxy-env fallback (POC)")
		return Flow{}, fmt.Errorf("CONNECT not supported")
	}

	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	if host == "" {
		return Flow{}, fmt.Errorf("proxy request without a destination host")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "80")
	}

	f := Flow{
		dst:  host,
		conn: conn,
		req:  req,
		br:   br,
	}
	if ap, err := netip.ParseAddrPort(host); err == nil {
		f.OrigDst = ap
	}
	return f, nil
}

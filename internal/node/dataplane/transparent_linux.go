//go:build linux

package dataplane

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"unsafe"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

// Linux netfilter constants for recovering the pre-REDIRECT destination.
const (
	solIP         = 0  // SOL_IP
	soOriginalDst = 80 // SO_ORIGINAL_DST
)

// NewTransparent starts the Linux transparent data plane: it listens for
// connections redirected to it by iptables REDIRECT, recovers each connection's
// original destination via SO_ORIGINAL_DST, and attributes the source to a
// process/session via the resolver.
func NewTransparent(addr string, resolver attest.Resolver, scope *proxyscope.Scope) (DataPlane, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := newInlinePlane(ln)
	p.logf("dataplane(transparent): listening on %s", addr)
	go p.serve(func(conn net.Conn) (Flow, error) {
		return buildTransparentFlow(conn, resolver, scope)
	})
	return p, nil
}

func buildTransparentFlow(conn net.Conn, resolver attest.Resolver, scope *proxyscope.Scope) (Flow, error) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return Flow{}, errors.New("transparent plane requires TCP connections")
	}
	dst, err := originalDestination(tcp)
	if err != nil {
		return Flow{}, err
	}
	if scope != nil && !scope.Managed(dst.String()) {
		if err := tunnelRaw(conn, dst.String()); err != nil {
			return Flow{}, err
		}
		return Flow{}, errFlowHandled
	}
	req, br, err := readRequest(conn)
	if err != nil {
		return Flow{}, err
	}

	f := Flow{
		OrigDst: dst,
		dst:     dst.String(),
		conn:    conn,
		req:     req,
		br:      br,
	}
	if scope == nil || scope.Managed(f.DstString()) {
		if err := applyRequestMetadata(&f, req); err != nil {
			f.MetadataError = err.Error()
		}
	}
	if resolver != nil {
		if src, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
			if attr, err := resolver.Resolve(src.IP.String(), src.Port); err == nil {
				f.SrcPID = attr.PID
				f.Cgroup = attr.Cgroup
				f.SessionID = attr.SessionID
				f.SVID = attr.SVID
				f.Routes = attr.Routes
			}
		}
	}
	return f, nil
}

// originalDestination recovers the original (pre-REDIRECT) destination of a
// transparently intercepted TCP connection via getsockopt(SO_ORIGINAL_DST).
func originalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}

	// struct sockaddr_in: family(2) port(2, big-endian) addr(4) pad(8) = 16 bytes.
	buf := make([]byte, 16)
	size := uint32(len(buf))

	var (
		out     netip.AddrPort
		sockErr error
	)
	ctrlErr := rc.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			uintptr(solIP),
			uintptr(soOriginalDst),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if errno != 0 {
			sockErr = errno
			return
		}
		port := uint16(buf[2])<<8 | uint16(buf[3])
		addr := netip.AddrFrom4([4]byte{buf[4], buf[5], buf[6], buf[7]})
		out = netip.AddrPortFrom(addr, port)
	})
	if ctrlErr != nil {
		return netip.AddrPort{}, ctrlErr
	}
	if sockErr != nil {
		return netip.AddrPort{}, fmt.Errorf("getsockopt(SO_ORIGINAL_DST): %w", sockErr)
	}
	return out, nil
}

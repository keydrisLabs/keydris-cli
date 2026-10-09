//go:build darwin

package deviceservice

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

const (
	darwinSOLLocal      = 0
	darwinLocalPeerCred = 1
)

type darwinXucred struct {
	Version uint32
	UID     uint32
	NGroups int16
	Groups  [16]uint32
}

func peerCredentials(connection net.Conn) (Caller, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return Caller{}, fmt.Errorf("device service connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return Caller{}, err
	}
	var credential darwinXucred
	size := uint32(unsafe.Sizeof(credential))
	var socketErr syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, socketErr = syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			darwinSOLLocal,
			darwinLocalPeerCred,
			uintptr(unsafe.Pointer(&credential)),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
	}); err != nil {
		return Caller{}, err
	}
	if socketErr != 0 {
		return Caller{}, socketErr
	}
	return Caller{UID: credential.UID}, nil
}

//go:build linux

package deviceservice

import (
	"fmt"
	"net"
	"syscall"
)

func peerCredentials(connection net.Conn) (Caller, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return Caller{}, fmt.Errorf("device service connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return Caller{}, err
	}
	var credential *syscall.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Caller{}, err
	}
	if socketErr != nil {
		return Caller{}, socketErr
	}
	return Caller{UID: credential.Uid, PID: credential.Pid}, nil
}

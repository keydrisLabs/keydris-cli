//go:build windows

package deviceservice

import (
	"fmt"
	"net"
	"os"
)

func validateSocketDirectory(string, uint32) error {
	return fmt.Errorf("device service named-pipe ACL validation is not available on Windows yet")
}

func validateSocketOwner(os.FileInfo, uint32) error {
	return fmt.Errorf("device service named-pipe ACL validation is not available on Windows yet")
}

func secureServiceSocket(string, uint32) error {
	return fmt.Errorf("device service named pipes are not available on Windows yet")
}

func peerCredentials(net.Conn) (Caller, error) {
	return Caller{}, fmt.Errorf("device service peer credentials are not available on Windows yet")
}

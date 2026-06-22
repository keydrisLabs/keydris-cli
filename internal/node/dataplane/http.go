package dataplane

import (
	"bufio"
	"net"
	"net/http"

	"github.com/nocaplabs/keydris-cli/internal/node/proxy"
)

// These thin wrappers keep the data planes decoupled from the shared L7 package
// (internal/node/proxy), which owns request reading, credential injection, and
// splicing for every OS-specific plane.

func readRequest(conn net.Conn) (*http.Request, *bufio.Reader, error) {
	return proxy.ReadRequest(conn)
}

func injectAndForward(client net.Conn, br *bufio.Reader, req *http.Request, dst string, c Credential) error {
	return proxy.InjectAndForward(client, br, req, dst, proxy.Credential(c))
}

func injectAndForwardTLS(client net.Conn, br *bufio.Reader, req *http.Request, dst, sni string, c Credential) error {
	return proxy.InjectAndForwardTLS(client, br, req, dst, sni, proxy.Credential(c))
}

func writeReject(client net.Conn, reason string) {
	proxy.WriteReject(client, reason)
}

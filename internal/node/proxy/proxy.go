// Package proxy holds the OS-agnostic L7 path shared by every data plane
// (plan.md section 3): read the buffered HTTP request, inject the credential,
// forward to the upstream, and splice bytes both ways. The OS-specific planes
// in internal/node/dataplane build a connection + destination and then delegate here.
package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Credential is the secret to inject. Only Type == "header" is supported.
type Credential struct {
	Type  string
	Name  string
	Value string
}

// ReadRequest reads the first HTTP/1.1 request from conn, returning the parsed
// request and the buffered reader (so bytes read past the head are not lost
// when splicing).
func ReadRequest(conn net.Conn) (*http.Request, *bufio.Reader, error) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, nil, err
	}
	return req, br, nil
}

// InjectAndForward injects the credential, writes the request to a freshly
// dialed upstream, and splices the remainder of the connection both ways.
//
// http.Request.Write emits origin-form (path from URL.RequestURI() + Host
// header) for both transparently intercepted and absolute-form proxy requests,
// so this works for the transparent and proxy-env planes alike.
func InjectAndForward(client net.Conn, br *bufio.Reader, req *http.Request, dst string, c Credential) error {
	if c.Type == "header" && c.Name != "" {
		req.Header.Set(c.Name, c.Value)
	}

	upstream, err := net.Dial("tcp", dst)
	if err != nil {
		WriteReject(client, "upstream unreachable")
		return fmt.Errorf("dial upstream %s: %w", dst, err)
	}
	defer upstream.Close()

	req.RequestURI = "" // required empty for the client-side Write path
	if err := req.Write(upstream); err != nil {
		return fmt.Errorf("write upstream request: %w", err)
	}

	splice(client, upstream, br)
	return nil
}

// InjectAndForwardTLS is the HTTPS-upstream variant used by the sandbox proxy
// after it has terminated the agent's TLS with a leaf from the Keydris CA. It
// re-originates a fresh TLS connection to the real upstream (sni names it),
// injects the credential, writes the request, and splices.
//
// The POC dials the upstream with InsecureSkipVerify because the demo backend
// uses a self-signed certificate; a real build would verify against the system
// roots (or pin the upstream).
func InjectAndForwardTLS(client net.Conn, br *bufio.Reader, req *http.Request, dst, sni string, c Credential) error {
	if c.Type == "header" && c.Name != "" {
		req.Header.Set(c.Name, c.Value)
	}

	upstream, err := tls.Dial("tcp", dst, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // POC: demo upstream is self-signed
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		WriteReject(client, "upstream unreachable")
		return fmt.Errorf("dial upstream (tls) %s: %w", dst, err)
	}
	defer upstream.Close()

	req.RequestURI = ""
	if err := req.Write(upstream); err != nil {
		return fmt.Errorf("write upstream request: %w", err)
	}

	splice(client, upstream, br)
	return nil
}

// WriteReject sends a synthetic 403 to the client without dialing upstream.
func WriteReject(client net.Conn, reason string) {
	if reason == "" {
		reason = "denied by keydris policy"
	}
	body := fmt.Sprintf("keydris: %s\n", reason)
	fmt.Fprintf(client,
		"HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(body), body)
}

// splice copies bytes bidirectionally until both directions hit EOF.
func splice(client, upstream net.Conn, clientReader io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		halfCloseWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		halfCloseWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func halfCloseWrite(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.CloseWrite()
	}
}

// Package proxy holds the OS-agnostic forwarding path shared by every data
// plane. Managed and plain-HTTP pass-through requests are forwarded one at a
// time; only opaque CONNECT tunnels splice bytes bidirectionally.
package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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

// InjectAndForwardOne forwards exactly one managed HTTP request. It deliberately
// closes the upstream connection after the response so a second client request
// cannot bypass authorization through the old byte-splice path.
func InjectAndForwardOne(client net.Conn, req *http.Request, dst string, c Credential) error {
	prepareOriginRequest(req)
	if c.Type == "header" && c.Name != "" {
		req.Header.Set(c.Name, c.Value)
	}

	upstream, err := net.Dial("tcp", dst)
	if err != nil {
		WriteReject(client, "upstream unreachable")
		return fmt.Errorf("dial upstream %s: %w", dst, err)
	}
	defer upstream.Close()

	req.RequestURI = ""
	req.Close = true
	req.Header.Set("Connection", "close")
	if err := req.Write(upstream); err != nil {
		return fmt.Errorf("write upstream request: %w", err)
	}
	return forwardOneResponse(client, upstream, req)
}

// InjectAndForwardTLSOne is the managed HTTPS equivalent of
// InjectAndForwardOne: authorize/inject once, then force connection close.
func InjectAndForwardTLSOne(client net.Conn, req *http.Request, dst, sni string, c Credential) error {
	prepareOriginRequest(req)
	if c.Type == "header" && c.Name != "" {
		req.Header.Set(c.Name, c.Value)
	}

	upstream, err := tls.Dial("tcp", dst, &tls.Config{
		ServerName: sni,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		WriteReject(client, "upstream unreachable")
		return fmt.Errorf("dial upstream (tls) %s: %w", dst, err)
	}
	defer upstream.Close()

	req.RequestURI = ""
	req.Close = true
	req.Header.Set("Connection", "close")
	if err := req.Write(upstream); err != nil {
		return fmt.Errorf("write upstream request: %w", err)
	}
	return forwardOneResponse(client, upstream, req)
}

// forwardOneResponse relays one upstream response to the client, rewriting the
// connection headers to announce the close. The forwarding path tears the
// socket down after a single request, so a client that trusted an upstream
// keep-alive header would pool the socket and have its next request reset
// before the proxy ever reads it.
func forwardOneResponse(client net.Conn, upstream net.Conn, req *http.Request) error {
	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}
	defer resp.Body.Close()

	// Response.Write emits `Connection: close` from resp.Close; the inherited
	// header would otherwise be written alongside it as a duplicate.
	resp.Header.Del("Connection")
	resp.Header.Del("Keep-Alive")
	resp.Close = true

	if err := resp.Write(client); err != nil {
		return fmt.Errorf("copy upstream response: %w", err)
	}
	return nil
}

// ForwardTLSOneTapped forwards exactly one HTTPS request unchanged (no
// credential injection), optionally teeing the response body into the writer
// tap returns for that response. The usage meter (ENG-261) reads token counts
// off the tee; the returned writer must never fail (its Write cannot error),
// so tapping can never break the forwarded stream.
func ForwardTLSOneTapped(
	client net.Conn,
	req *http.Request,
	dst, sni string,
	tap func(*http.Response) io.Writer,
	clientReaders ...io.Reader,
) error {
	prepareOriginRequest(req)

	upstream, err := tls.Dial("tcp", dst, &tls.Config{
		ServerName: sni,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		WriteReject(client, "upstream unreachable")
		return fmt.Errorf("dial upstream (tls) %s: %w", dst, err)
	}
	defer upstream.Close()

	return forwardOneTapped(client, req, upstream, tap, clientReaders...)
}

func forwardOneTapped(client net.Conn, req *http.Request, upstream net.Conn, tap func(*http.Response) io.Writer, clientReaders ...io.Reader) error {
	req.RequestURI = ""
	upgrade := strings.EqualFold(req.Header.Get("Upgrade"), "websocket") && headerToken(req.Header.Get("Connection"), "upgrade")
	if !upgrade {
		req.Close = true
		req.Header.Set("Connection", "close")
	}
	if err := req.Write(upstream); err != nil {
		return fmt.Errorf("write upstream request: %w", err)
	}

	upstreamReader := bufio.NewReader(upstream)
	resp, err := http.ReadResponse(upstreamReader, req)
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}
	defer resp.Body.Close()
	if upgrade && resp.StatusCode == http.StatusSwitchingProtocols {
		// A WebSocket is bidirectional and may contain buffered frames on both
		// sides already. Preserve its handshake and forward all subsequent bytes.
		if err := resp.Write(client); err != nil {
			return err
		}
		var clientReader io.Reader = client
		if len(clientReaders) > 0 && clientReaders[0] != nil {
			clientReader = clientReaders[0]
		}
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(upstream, clientReader); done <- struct{}{} }()
		go func() { _, _ = io.Copy(client, upstreamReader); done <- struct{}{} }()
		<-done
		_ = upstream.Close()
		_ = client.Close()
		<-done
		return nil
	}
	if tap != nil {
		if sink := tap(resp); sink != nil {
			resp.Body = teedBody{
				Reader: io.TeeReader(resp.Body, sink),
				closer: resp.Body,
			}
		}
	}

	resp.Header.Del("Connection")
	resp.Header.Del("Keep-Alive")
	resp.Close = true
	if err := resp.Write(client); err != nil {
		return fmt.Errorf("copy upstream response: %w", err)
	}
	return nil
}

func headerToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

type teedBody struct {
	io.Reader
	closer io.Closer
}

func (b teedBody) Close() error { return b.closer.Close() }

// TunnelCONNECT establishes an opaque HTTP CONNECT tunnel. Keydris does not
// terminate TLS, inspect request bodies, authorize, or mutate headers on it.
func TunnelCONNECT(client net.Conn, bufferedClient io.Reader, target string) error {
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		WriteReject(client, "upstream unreachable")
		return fmt.Errorf("dial tunnel upstream %s: %w", target, err)
	}
	defer upstream.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return err
	}
	splice(client, upstream, bufferedClient)
	return nil
}

// TunnelRaw establishes an opaque tunnel for transparently intercepted traffic
// that has no HTTP CONNECT preamble.
func TunnelRaw(client net.Conn, clientReader io.Reader, target string) error {
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return fmt.Errorf("dial tunnel upstream %s: %w", target, err)
	}
	defer upstream.Close()
	splice(client, upstream, clientReader)
	return nil
}

func prepareOriginRequest(req *http.Request) {
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
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

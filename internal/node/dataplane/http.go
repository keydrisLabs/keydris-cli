package dataplane

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"

	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// These thin wrappers keep the data planes decoupled from the shared L7 package
// (internal/node/proxy), which owns request reading, credential injection, and
// splicing for every OS-specific plane.

func readRequest(conn net.Conn) (*http.Request, *bufio.Reader, error) {
	return proxy.ReadRequest(conn)
}

func injectAndForward(client net.Conn, br *bufio.Reader, req *http.Request, dst string, c Credential) error {
	return proxy.InjectAndForwardOne(client, req, dst, proxy.Credential(c))
}

func injectAndForwardTLS(client net.Conn, br *bufio.Reader, req *http.Request, dst, sni string, c Credential) error {
	return proxy.InjectAndForwardTLSOne(client, req, dst, sni, proxy.Credential(c))
}

func forwardUnchanged(client net.Conn, br *bufio.Reader, req *http.Request, dst string) error {
	return proxy.InjectAndForwardOne(client, req, dst, proxy.Credential{})
}

func forwardUnchangedTLS(client net.Conn, br *bufio.Reader, req *http.Request, dst, sni string) error {
	return proxy.InjectAndForwardTLSOne(client, req, dst, sni, proxy.Credential{})
}

func tunnelCONNECT(client net.Conn, br *bufio.Reader, target string) error {
	return proxy.TunnelCONNECT(client, br, target)
}

func tunnelRaw(client net.Conn, target string) error {
	return proxy.TunnelRaw(client, client, target)
}

func writeReject(client net.Conn, reason string) {
	proxy.WriteReject(client, reason)
}

func writeProviderResponse(
	client net.Conn,
	provider runtimecontract.ProviderHTTPResponse,
) error {
	headers := make(http.Header)
	for name, value := range provider.Headers {
		switch http.CanonicalHeaderKey(name) {
		case "Content-Type", "Etag", "Last-Modified", "Link", "Location",
			"Retry-After", "X-Github-Api-Version-Selected",
			"X-Github-Request-Id", "X-Ratelimit-Limit",
			"X-Ratelimit-Remaining", "X-Ratelimit-Reset",
			"X-Ratelimit-Resource", "X-Ratelimit-Used",
			"X-Slack-Req-Id":
			headers.Set(name, value)
		}
	}
	body := provider.Body
	if len(body) == 0 {
		body = []byte("null")
	}
	response := &http.Response{
		StatusCode:    provider.Status,
		Status:        http.StatusText(provider.Status),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        headers,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	return response.Write(client)
}

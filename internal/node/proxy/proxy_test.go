package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTunnelCONNECTPassesBytesUnchanged(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		body := make([]byte, 4)
		_, _ = io.ReadFull(conn, body)
		received <- string(body)
		_, _ = conn.Write([]byte("pong"))
	}()

	client, proxySide := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- TunnelCONNECT(proxySide, bufio.NewReader(proxySide), upstream.Addr().String())
	}()

	reader := bufio.NewReader(client)
	line, err := reader.ReadString('\n')
	if err != nil || line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("CONNECT response = %q, err=%v", line, err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_, _ = client.Write([]byte("ping"))
	reply := make([]byte, 4)
	if _, err := io.ReadFull(reader, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "pong" {
		t.Fatalf("reply = %q", reply)
	}
	if got := <-received; got != "ping" {
		t.Fatalf("upstream received %q", got)
	}
	_ = client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tunnel did not close")
	}
}

func TestInjectAndForwardOneInjectsAndCloses(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	gotHeaders := make(chan [2]string, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		gotHeaders <- [2]string{req.Header.Get("X-Keydris-Test"), req.Header.Get("Proxy-Authorization")}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	}()

	req, err := http.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Proxy-Authorization", "Basic session-secret")
	client, proxySide := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- InjectAndForwardOne(proxySide, req, upstream.Addr().String(), Credential{
			Type: "header", Name: "X-Keydris-Test", Value: "injected",
		})
		_ = proxySide.Close()
	}()

	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response), "\r\n\r\nok") {
		t.Fatalf("response = %q", response)
	}
	if got := <-gotHeaders; got[0] != "injected" || got[1] != "" {
		t.Fatalf("upstream headers = injected %q, proxy authorization %q", got[0], got[1])
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The forwarding path serves one request per connection. An upstream keep-alive
// header reaching the client would invite it to pool the socket and have the
// next request reset before the proxy reads it.
func TestInjectAndForwardOneOverridesUpstreamKeepAlive(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n"+
			"Connection: keep-alive\r\nKeep-Alive: timeout=5\r\n\r\nok")
	}()

	req, err := http.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	client, proxySide := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- InjectAndForwardOne(proxySide, req, upstream.Addr().String(), Credential{})
		_ = proxySide.Close()
	}()

	raw, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(string(raw), "Connection:"); n != 1 {
		t.Fatalf("want exactly one Connection header, got %d in %q", n, raw)
	}
	if strings.Contains(string(raw), "Keep-Alive:") {
		t.Fatalf("Keep-Alive header survived: %q", raw)
	}

	resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(string(raw))), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !resp.Close {
		t.Fatal("response did not announce close to the client")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

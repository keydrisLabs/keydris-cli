package desktopproxy

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCONNECTStampsAuthAndPreservesTarget(t *testing.T) {
	const handle = "session-handle-1"
	const target = "example.com:443"

	gotAuth := make(chan string, 1)
	gotURI := make(chan string, 1)
	upstream := listenUpstream(t, func(conn net.Conn) {
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		gotAuth <- req.Header.Get("Proxy-Authorization")
		gotURI <- req.RequestURI
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		if string(buf) != "ping" {
			return
		}
		_, _ = conn.Write([]byte("pong"))
	})
	defer upstream.Close()

	srv, err := Listen(upstream.Addr().String(), handle)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	client, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, _ = io.WriteString(client, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")

	br := bufio.NewReader(client)
	line, err := br.ReadString('\n')
	if err != nil || line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("CONNECT response = %q, err=%v", line, err)
	}
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	_, _ = client.Write([]byte("ping"))
	reply := make([]byte, 4)
	if _, err := io.ReadFull(br, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "pong" {
		t.Fatalf("reply = %q", reply)
	}

	select {
	case auth := <-gotAuth:
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if err != nil {
			t.Fatalf("decode auth %q: %v", auth, err)
		}
		if string(raw) != "keydris:"+handle {
			t.Fatalf("decoded Proxy-Authorization = %q", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive CONNECT")
	}
	select {
	case uri := <-gotURI:
		if uri != target {
			t.Fatalf("RequestURI = %q, want %q", uri, target)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not report RequestURI")
	}
}

func TestCONNECTReplacesExistingProxyAuthorization(t *testing.T) {
	const handle = "real-handle"

	gotAuth := make(chan string, 1)
	upstream := listenUpstream(t, func(conn net.Conn) {
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		gotAuth <- req.Header.Get("Proxy-Authorization")
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	})
	defer upstream.Close()

	srv, err := Listen(upstream.Addr().String(), handle)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	client, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, _ = io.WriteString(client,
		"CONNECT example.com:443 HTTP/1.1\r\n"+
			"Host: example.com:443\r\n"+
			"Proxy-Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("other:stale"))+"\r\n"+
			"\r\n")

	select {
	case auth := <-gotAuth:
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if err != nil {
			t.Fatalf("decode auth %q: %v", auth, err)
		}
		if string(raw) != "keydris:"+handle {
			t.Fatalf("decoded Proxy-Authorization = %q", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive CONNECT")
	}
}

func TestClientAllowed(t *testing.T) {
	if clientAllowed(&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 1234}) {
		t.Fatal("10.1.2.3:1234 should be rejected")
	}
	if !clientAllowed(&net.TCPAddr{IP: net.ParseIP("127.0.0.1")}) {
		t.Fatal("127.0.0.1 should be accepted")
	}
	if !clientAllowed(&net.TCPAddr{IP: net.ParseIP("::1"), Port: 9}) {
		t.Fatal("::1 should be accepted")
	}
}

func TestListenRejectsEmptyHandle(t *testing.T) {
	_, err := Listen("127.0.0.1:15001", "")
	if err == nil {
		t.Fatal("expected error for empty handle")
	}
}

func listenUpstream(t *testing.T, handle func(net.Conn)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn)
	}()
	return ln
}

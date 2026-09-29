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

func TestMeteredWebSocketPreservesUpgradeAndBufferedFrames(t *testing.T) {
	client, proxyClient := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	defer client.Close()
	defer proxyClient.Close()
	defer proxyUpstream.Close()
	defer upstream.Close()
	for _, c := range []net.Conn{client, proxyClient, proxyUpstream, upstream} {
		c.SetDeadline(time.Now().Add(3 * time.Second))
	}
	req, _ := http.NewRequest("GET", "https://api.openai.com/v1/responses", nil)
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	serverDone := make(chan error, 1)
	go func() {
		defer upstream.Close()
		r := bufio.NewReader(upstream)
		observed, err := http.ReadRequest(r)
		if err != nil {
			serverDone <- err
			return
		}
		if observed.Header.Get("Upgrade") != "websocket" || !headerToken(observed.Header.Get("Connection"), "upgrade") {
			t.Error("upgrade was stripped")
		}
		_, err = io.WriteString(upstream, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nserver-frame")
		if err != nil {
			serverDone <- err
			return
		}
		b := make([]byte, len("client-frame"))
		_, err = io.ReadFull(r, b)
		if err == nil {
			_, err = upstream.Write(b)
		}
		serverDone <- err
	}()
	forwardDone := make(chan error, 1)
	go func() {
		forwardDone <- forwardOneTapped(proxyClient, req, proxyUpstream, func(*http.Response) io.Writer { t.Error("websocket must not be metered as HTTP usage"); return nil }, io.MultiReader(strings.NewReader("client-frame"), proxyClient))
	}()
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 101 || response.Header.Get("Upgrade") != "websocket" {
		t.Fatalf("invalid upgrade: %+v", response)
	}
	b := make([]byte, len("server-frameclient-frame"))
	if _, err := io.ReadFull(reader, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "server-frameclient-frame" {
		t.Fatal(string(b))
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if err := <-forwardDone; err != nil {
		t.Fatal(err)
	}
}

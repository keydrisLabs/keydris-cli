package login

import (
	"net"
	"testing"
)

func TestLocalhostCallbackUsesIPv4AndPreservesRedirect(t *testing.T) {
	opt := Options{RedirectURL: "http://localhost:0/callback"}
	listener, redirect, path, err := opt.listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	if !address.IP.Equal(net.ParseIP("127.0.0.1")) || redirect != opt.RedirectURL || path != "/callback" {
		t.Fatalf("callback changed provider redirect or did not bind IPv4 loopback: %v %s %s", address, redirect, path)
	}
}

func TestCallbackRefusesNonLoopbackBindings(t *testing.T) {
	for _, address := range []string{"http://0.0.0.0:0/callback", "http://[::]:0/callback", "http://example.com:3000/callback", "https://localhost:3000/callback"} {
		opt := Options{RedirectURL: address}
		listener, _, _, err := opt.listen()
		if listener != nil {
			listener.Close()
		}
		if err == nil {
			t.Errorf("accepted callback %s", address)
		}
	}
}

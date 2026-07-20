package dataplane

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
)

func reqWithToken(token string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	if token != "" {
		cred := base64.StdEncoding.EncodeToString([]byte("keydris:" + token))
		r.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	return r
}

func TestMatchSessionTokenAndSoleGate(t *testing.T) {
	reg := attest.NewSessionRegistry()
	reg.Register(attest.Session{Handle: "tokA", SPIFFEID: "spiffe://a", SVID: "svidA"})
	reg.Register(attest.Session{Handle: "tokB", SPIFFEID: "spiffe://b", SVID: "svidB"})

	// Token match resolves the exact session, even with several registered.
	p := &sandboxPlane{reg: reg, allowSole: false}
	if s := p.matchSession(reqWithToken("tokB")); s == nil || s.SPIFFEID != "spiffe://b" {
		t.Errorf("token match failed: %+v", s)
	}
	// Unknown token is never downgraded to a registered session.
	if s := p.matchSession(reqWithToken("nope")); s != nil {
		t.Errorf("unknown token should be unattributed, got %+v", s)
	}
	// Tokenless with Sole gate OFF (default) is unattributed.
	if s := p.matchSession(reqWithToken("")); s != nil {
		t.Errorf("tokenless with Sole disabled should be nil, got %+v", s)
	}

	// Tokenless with Sole gate ON resolves only when exactly one session exists.
	single := attest.NewSessionRegistry()
	single.Register(attest.Session{Handle: "only", SPIFFEID: "spiffe://only", SVID: "s"})
	ps := &sandboxPlane{reg: single, allowSole: true}
	if s := ps.matchSession(reqWithToken("")); s == nil || s.SPIFFEID != "spiffe://only" {
		t.Errorf("tokenless Sole fallback failed: %+v", s)
	}
	// Two sessions + Sole ON + no token: ambiguous -> still unattributed.
	pa := &sandboxPlane{reg: reg, allowSole: true}
	if s := pa.matchSession(reqWithToken("")); s != nil {
		t.Errorf("ambiguous tokenless should be nil, got %+v", s)
	}
}

func TestVerifyPeerNoopWhenOwnerUnknown(t *testing.T) {
	// OwnerPID == 0 means "can't verify" -> allow regardless of mode.
	p := &sandboxPlane{peerVerify: PeerVerifyEnforce, logf: func(string, ...any) {}}
	if !p.verifyPeer(nil, &attest.Session{SPIFFEID: "x"}) {
		t.Errorf("verifyPeer should allow when OwnerPID is unknown")
	}
}

func TestRequestDestinationRejectsAuthorityMismatch(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://managed.example/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "unmanaged.example"
	if _, err := requestDestination(req, "http"); err == nil {
		t.Fatal("expected URL/Host authority mismatch")
	}
}

func TestRequestDestinationCanonicalizesOriginForm(t *testing.T) {
	req := &http.Request{Host: "Managed.Example.", URL: &url.URL{Path: "/mcp"}}
	got, err := requestDestination(req, "https")
	if err != nil {
		t.Fatal(err)
	}
	if got != "managed.example:443" {
		t.Fatalf("destination = %q", got)
	}
}

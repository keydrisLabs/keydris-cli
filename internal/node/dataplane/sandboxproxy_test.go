package dataplane

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/meter"
	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func reqWithToken(token string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	if token != "" {
		cred := base64.StdEncoding.EncodeToString([]byte("keydris:" + token))
		r.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	return r
}

func TestPolicyManagedOriginsStillTakePrecedenceOverMetering(t *testing.T) {
	scope, err := proxyscope.New(proxyscope.ModeSelected, []string{"chatgpt.com:443", "api.openai.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	p := &sandboxPlane{scope: scope, meterOrigins: meter.NewOrigins(nil)}
	for _, host := range []string{"chatgpt.com", "api.openai.com"} {
		if !p.managesSessionOrigin(nil, "https", host, 443) {
			t.Fatalf("metered %s bypassed policy scope", host)
		}
		if _, metered := p.meterOrigins.Provider(host, 443); !metered {
			t.Fatal("test did not cover overlap")
		}
	}
}

func TestMeteredManagedCONNECTStillReachesPolicyDenial(t *testing.T) {
	// A loopback-only origin exercises the same overlapping route without
	// ever contacting a provider, even if routing regresses.
	scope, err := proxyscope.New(proxyscope.ModeSelected, []string{"127.0.0.1:443"})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := proxy.GenerateCA("policy test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reg := attest.NewSessionRegistry()
	reg.Register(attest.Session{Handle: "test-session", SVID: "test-kit"})
	m := meter.New(http.DefaultClient, "http://127.0.0.1:1", reg, t.Logf)
	defer m.Close()
	p := &sandboxPlane{scope: scope, ca: ca, reg: reg, meter: m,
		meterOrigins: meter.NewOrigins([]string{"127.0.0.1=openai"}), logf: t.Logf, leaves: map[string]*tls.Certificate{}}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	server.SetDeadline(time.Now().Add(3 * time.Second))
	denied := make(chan error, 1)
	go func() {
		flow, ok := p.build(server)
		if !ok {
			denied <- fmt.Errorf("managed request bypassed policy routing")
			return
		}
		denied <- p.Reject(flow, "test policy denial")
	}()
	credential := base64.StdEncoding.EncodeToString([]byte("keydris:test-session"))
	if _, err := fmt.Fprintf(client, "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\nProxy-Authorization: Basic %s\r\n\r\n", credential); err != nil {
		t.Fatal(err)
	}
	connect, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil || connect.StatusCode != http.StatusOK {
		t.Fatal(connect, err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM())
	secured := tls.Client(client, &tls.Config{ServerName: "127.0.0.1", RootCAs: roots, MinVersion: tls.VersionTLS12})
	if _, err := fmt.Fprint(secured, "POST /v1/responses HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 2\r\n\r\n{}"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(secured), nil)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatal(response, err)
	}
	response.Body.Close()
	client.Close()
	if err := <-denied; err != nil {
		t.Fatal(err)
	}
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

func TestRoutesOriginOverridesLegacyManualScope(t *testing.T) {
	legacy, err := proxyscope.New(
		proxyscope.ModeSelected,
		[]string{"legacy.example:443"},
	)
	if err != nil {
		t.Fatal(err)
	}
	reason := "keydris_action_unsupported"
	routes := runtimecontract.RuntimeRoutes{
		SchemaVersion:  1,
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		Agent: runtimecontract.RoutesAgent{
			AgentID:     "33333333-3333-4333-8333-333333333333",
			DisplayName: "Test agent",
		},
		Policy: &runtimecontract.RoutesPolicy{
			PolicyID:        "55555555-5555-4555-8555-555555555555",
			PolicyVersionID: "66666666-6666-4666-8666-666666666666",
			PolicyHash:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Routes: []runtimecontract.RuntimeRoute{
			{
				RouteID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				DisplayName:      "Managed provider",
				Provider:         "github",
				ConnectionID:     "77777777-7777-4777-8777-777777777777",
				EnforcementMode:  "provider_executor",
				Availability:     "unavailable",
				StatusReasonCode: &reason,
				Matchers: []runtimecontract.RouteMatcher{
					{
						MatcherType: "http.origin",
						Attributes:  json.RawMessage(`{"scheme":"https","host":"api.github.com","port":443,"path_prefix":"/"}`),
					},
				},
				RuntimeEndpointPath: "/v1/runtime/providers/github/execute",
			},
		},
	}
	if err := routes.Validate(); err != nil {
		t.Fatal(err)
	}
	session := &attest.Session{Routes: &routes}
	proxy := &sandboxPlane{scope: legacy}
	if !proxy.managesSessionOrigin(session, "https", "api.github.com", 443) {
		t.Fatal("routes origin was not managed")
	}
	if proxy.managesSessionOrigin(session, "https", "legacy.example", 443) {
		t.Fatal("legacy scope overrode the session routes")
	}
	if !proxy.managesSessionOrigin(nil, "https", "legacy.example", 443) {
		t.Fatal("legacy scope was not retained for a pre-routes session")
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

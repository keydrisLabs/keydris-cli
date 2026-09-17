package dataplane

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
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

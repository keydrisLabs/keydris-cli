package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/authz"
	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/evidence"
	"github.com/keydrisLabs/keydris-cli/internal/node/dataplane"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

type fakeDataPlane struct {
	injected      bool
	passedThrough bool
	rejected      bool
}

func (p *fakeDataPlane) Flows() <-chan dataplane.Flow { return nil }
func (p *fakeDataPlane) Close() error                 { return nil }
func (p *fakeDataPlane) Inject(dataplane.Flow, dataplane.Credential) error {
	p.injected = true
	return nil
}
func (p *fakeDataPlane) PassThrough(dataplane.Flow) error {
	p.passedThrough = true
	return nil
}
func (p *fakeDataPlane) Reject(dataplane.Flow, string) error {
	p.rejected = true
	return nil
}

func TestHandleFlowAuditsManagedToolWithoutSecrets(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request authz.AuthorizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode authorize request: %v", err)
		}
		if request.ToolCall != "list_users" || string(request.ToolParams) != `{"limit":3,"note":"svid-secret"}` {
			t.Errorf("tool metadata = %q %s", request.ToolCall, request.ToolParams)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"allow","reason":"svid-secret injected-secret","inject":{"type":"header","name":"Authorization","value":"injected-secret"}}`))
	}))
	defer server.Close()

	ledgerPath := t.TempDir() + "/evidence.jsonl"
	ledger, err := evidence.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := proxyscope.New(proxyscope.ModeAll, nil)
	dp := &fakeDataPlane{}
	flow := dataplane.Flow{
		OrigDst:    netip.MustParseAddrPort("192.0.2.10:443"),
		SessionID:  "spiffe://keydris.test/session/1",
		SVID:       "svid-secret",
		ToolCall:   "list_users",
		ToolParams: json.RawMessage(`{"limit":3,"note":"svid-secret"}`),
	}
	handleFlow(context.Background(), &config.Config{ControlMTLSURL: server.URL, PolicyID: "test"}, server.Client(), dp, scope, ledger, flow)

	if calls != 1 || !dp.injected || dp.passedThrough || dp.rejected {
		t.Fatalf("calls=%d injected=%v passed=%v rejected=%v", calls, dp.injected, dp.passedThrough, dp.rejected)
	}
	records, err := evidence.Read(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	payload := string(records[0].Payload)
	if !strings.Contains(payload, `"tool_call":"list_users"`) || !strings.Contains(payload, `"limit":3`) {
		t.Fatalf("audit missing full tool metadata: %s", payload)
	}
	if strings.Contains(payload, "svid-secret") || strings.Contains(payload, "injected-secret") {
		t.Fatalf("audit leaked a credential: %s", payload)
	}
}

func TestHandleFlowPassesUnmanagedWithoutBroker(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	defer server.Close()

	scope, err := proxyscope.New(proxyscope.ModeSelected, []string{"198.51.100.1:443"})
	if err != nil {
		t.Fatal(err)
	}
	dp := &fakeDataPlane{}
	flow := dataplane.Flow{OrigDst: netip.MustParseAddrPort("192.0.2.10:443")}
	handleFlow(context.Background(), &config.Config{ControlMTLSURL: server.URL}, server.Client(), dp, scope, nil, flow)

	if calls != 0 || !dp.passedThrough || dp.injected || dp.rejected {
		t.Fatalf("calls=%d injected=%v passed=%v rejected=%v", calls, dp.injected, dp.passedThrough, dp.rejected)
	}
}

func TestTwoMCPCallsAuthorizeIndependently(t *testing.T) {
	var requests []authz.AuthorizeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request authz.AuthorizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode authorize request: %v", err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"allow"}`))
	}))
	defer server.Close()

	scope, _ := proxyscope.New(proxyscope.ModeAll, nil)
	cfg := &config.Config{ControlMTLSURL: server.URL}
	for i, params := range []string{`{"limit":1}`, `{"limit":2}`} {
		flow := dataplane.Flow{
			OrigDst:    netip.MustParseAddrPort("192.0.2.10:443"),
			ToolCall:   "list_users",
			ToolParams: json.RawMessage(params),
		}
		dp := &fakeDataPlane{}
		handleFlow(context.Background(), cfg, server.Client(), dp, scope, nil, flow)
		if !dp.injected {
			t.Fatalf("call %d was not forwarded", i+1)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("authorize requests = %d", len(requests))
	}
	if string(requests[0].ToolParams) != `{"limit":1}` || string(requests[1].ToolParams) != `{"limit":2}` {
		t.Fatalf("tool params = %s, %s", requests[0].ToolParams, requests[1].ToolParams)
	}
}

func TestHandleFlowFailsClosedWhenAuditAppendFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"allow"}`))
	}))
	defer server.Close()

	base := t.TempDir() + "/ledger"
	ledger, err := evidence.Open(base + "/evidence.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	scope, _ := proxyscope.New(proxyscope.ModeAll, nil)
	dp := &fakeDataPlane{}
	flow := dataplane.Flow{OrigDst: netip.MustParseAddrPort("192.0.2.10:443")}
	handleFlow(context.Background(), &config.Config{ControlMTLSURL: server.URL}, server.Client(), dp, scope, ledger, flow)
	if !dp.rejected || dp.injected {
		t.Fatalf("audit failure rejected=%v injected=%v", dp.rejected, dp.injected)
	}
}

func TestHandleFlowAuditsMetadataValidationDenial(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	defer server.Close()
	ledgerPath := t.TempDir() + "/evidence.jsonl"
	ledger, err := evidence.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}

	scope, _ := proxyscope.New(proxyscope.ModeAll, nil)
	dp := &fakeDataPlane{}
	flow := dataplane.Flow{
		OrigDst:       netip.MustParseAddrPort("192.0.2.10:443"),
		ToolCall:      "POST /mcp",
		MetadataError: "tool params exceed 1048576 bytes",
	}
	handleFlow(context.Background(), &config.Config{ControlMTLSURL: server.URL}, server.Client(), dp, scope, ledger, flow)
	if calls != 0 || !dp.rejected || dp.injected {
		t.Fatalf("calls=%d rejected=%v injected=%v", calls, dp.rejected, dp.injected)
	}
	records, err := evidence.Read(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !strings.Contains(string(records[0].Payload), `"decision":"deny"`) {
		t.Fatalf("audit records = %+v", records)
	}
}

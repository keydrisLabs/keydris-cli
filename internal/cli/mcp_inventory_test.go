package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// inventoryReportServer records the MCP inventory reports the CLI delivers and
// can force a per-runtime HTTP failure so the best-effort contract is testable.
type inventoryReportServer struct {
	*httptest.Server
	mu      sync.Mutex
	reports map[string]runtimecontract.MCPInventoryReport
	status  map[string]int
}

func newInventoryReportServer(t *testing.T) *inventoryReportServer {
	t.Helper()
	recorder := &inventoryReportServer{
		reports: map[string]runtimecontract.MCPInventoryReport{},
		status:  map[string]int{},
	}
	recorder.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/runtime/devices/mcp-inventory" {
			t.Errorf("inventory request = %s %s, want POST the inventory path", r.Method, r.URL.Path)
		}
		var report runtimecontract.MCPInventoryReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode inventory report: %v", err)
		}
		recorder.mu.Lock()
		recorder.reports[report.Runtime] = report
		status := recorder.status[report.Runtime]
		recorder.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recorder.Close)
	return recorder
}

func (s *inventoryReportServer) report(runtimeName string) (runtimecontract.MCPInventoryReport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	report, ok := s.reports[runtimeName]
	return report, ok
}

// installInventoryIdentity provisions the device identity mTLSClient requires.
// The stored certificate outlives the renewal window, so reporting never tries
// to renew, and the test server's own certificate is the pinned server CA.
func installInventoryIdentity(t *testing.T, cfg *config.Config, server *httptest.Server) {
	t.Helper()
	cfg.ControlMTLSURL = server.URL
	cfg.MTLSServerCA = filepath.Join(t.TempDir(), "server-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(cfg.MTLSServerCA, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "keydris-inventory-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(72 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.IdentityDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cfg.IdentityDir, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(login.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	write(login.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	meta, err := json.Marshal(login.Identity{
		Email:    "inventory@example.com",
		DeviceID: "11111111-1111-4111-8111-111111111111",
		NotAfter: template.NotAfter.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	write(login.WhoamiFile, append(meta, '\n'), 0o644)
}

func writeInventoryConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReportMCPInventoriesUploadsEachRuntime pins the session-start reporting
// path added with MCP observability: both runtimes' metadata reaches the
// control plane over the device identity, tagged and timestamped per runtime.
func TestReportMCPInventoriesUploadsEachRuntime(t *testing.T) {
	server := newInventoryReportServer(t)
	cfg := uxConfig(t)
	installInventoryIdentity(t, cfg, server.Server)
	writeInventoryConfig(t, cfg.ClaudeMcpConfigPath, `{"mcpServers":{"claude-server":{"url":"https://mcp.example.com/mcp"}}}`)
	writeInventoryConfig(t, cfg.CodexConfigPath, `[mcp_servers.codex-server]
url = "https://codex.example.com/mcp"
`)

	var out strings.Builder
	reportMCPInventories(cfg, &out)
	if out.Len() != 0 {
		t.Fatalf("successful reporting wrote %q", out.String())
	}
	for runtimeName, wantEntry := range map[string]string{
		"claude_code": "claude-server",
		"codex":       "codex-server",
	} {
		report, ok := server.report(runtimeName)
		if !ok {
			t.Fatalf("no %s inventory report was delivered", runtimeName)
		}
		if report.SchemaVersion != runtimecontract.SchemaVersion || report.Runtime != runtimeName {
			t.Fatalf("%s report envelope = %+v", runtimeName, report)
		}
		if _, err := time.Parse(time.RFC3339Nano, report.ObservedAt); err != nil {
			t.Fatalf("%s observed_at = %q: %v", runtimeName, report.ObservedAt, err)
		}
		if len(report.Entries) != 1 || report.Entries[0].Name != wantEntry {
			t.Fatalf("%s entries = %+v", runtimeName, report.Entries)
		}
	}
}

// TestReportMCPInventoriesKeepsOtherRuntimeOnBrokenConfig pins the independent
// best-effort contract: an unreadable file keeps the previous server-side
// snapshot for that runtime and must not suppress the other runtime's report.
func TestReportMCPInventoriesKeepsOtherRuntimeOnBrokenConfig(t *testing.T) {
	server := newInventoryReportServer(t)
	cfg := uxConfig(t)
	installInventoryIdentity(t, cfg, server.Server)
	writeInventoryConfig(t, cfg.ClaudeMcpConfigPath, `{broken`)
	writeInventoryConfig(t, cfg.CodexConfigPath, `[mcp_servers.codex-server]
command = "codex-mcp"
`)

	var out strings.Builder
	reportMCPInventories(cfg, &out)

	if !strings.Contains(out.String(), "claude_code MCP inventory unavailable; keeping the previous report") {
		t.Fatalf("missing claude_code failure message: %q", out.String())
	}
	if strings.Contains(out.String(), "codex MCP inventory") {
		t.Fatalf("codex reporting was suppressed: %q", out.String())
	}
	if _, ok := server.report("claude_code"); ok {
		t.Fatal("broken Claude configuration was uploaded")
	}
	report, ok := server.report("codex")
	if !ok || len(report.Entries) != 1 || report.Entries[0].Transport != "stdio" {
		t.Fatalf("codex report = %+v, delivered=%v", report, ok)
	}
}

// TestReportMCPInventoriesReportsDeliveryFailure pins that a rejected upload is
// logged for retry on the next session while the remaining runtime still runs.
func TestReportMCPInventoriesReportsDeliveryFailure(t *testing.T) {
	server := newInventoryReportServer(t)
	server.status["claude_code"] = http.StatusServiceUnavailable
	cfg := uxConfig(t)
	installInventoryIdentity(t, cfg, server.Server)
	writeInventoryConfig(t, cfg.ClaudeMcpConfigPath, `{"mcpServers":{}}`)
	writeInventoryConfig(t, cfg.CodexConfigPath, "")

	var out strings.Builder
	reportMCPInventories(cfg, &out)

	if !strings.Contains(out.String(), "claude_code MCP inventory delivery failed; will retry on the next session") {
		t.Fatalf("missing claude_code delivery message: %q", out.String())
	}
	if _, ok := server.report("codex"); !ok {
		t.Fatalf("codex reporting did not run after the Claude failure: %q", out.String())
	}
}

// TestReportMCPInventoriesWithoutIdentityDoesNotCallTheControlPlane covers the
// pre-login startup path: the hook stays best-effort and never fails the
// session when no device identity is available.
func TestReportMCPInventoriesWithoutIdentityDoesNotCallTheControlPlane(t *testing.T) {
	server := newInventoryReportServer(t)
	cfg := uxConfig(t)

	var out strings.Builder
	reportMCPInventories(cfg, &out)
	if !strings.Contains(out.String(), "could not authenticate; keeping the previous report") {
		t.Fatalf("missing authentication message: %q", out.String())
	}
	if _, ok := server.report("claude_code"); ok {
		t.Fatal("unauthenticated inventory reached the control plane")
	}
	if _, ok := server.report("codex"); ok {
		t.Fatal("unauthenticated inventory reached the control plane")
	}
}

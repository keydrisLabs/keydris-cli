package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNPMManagedUpgradePrintsPackageManagerCommand(t *testing.T) {
	t.Setenv("KEYDRIS_DISTRIBUTION", "npm")
	var output bytes.Buffer
	if !printNPMUpgradeInstructions(&output) {
		t.Fatal("npm-managed installation was not detected")
	}
	if !strings.Contains(output.String(), "npm install --global @keydris/cli@latest") {
		t.Fatalf("missing npm upgrade command: %s", output.String())
	}
}

func TestHTTPGetLimitRejectsInsecureRemoteURL(t *testing.T) {
	if _, err := httpGetLimit("http://example.com/keydris", 1024); err == nil {
		t.Fatal("expected insecure remote URL to be rejected")
	}
}

func TestHTTPGetLimitBoundsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()

	if _, err := httpGetLimit(server.URL, 4); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected response-size error, got %v", err)
	}
}

func TestHTTPGetLimitRejectsInsecureRedirect(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/artifact", http.StatusFound)
	}))
	defer redirector.Close()
	if _, err := httpGetLimit(redirector.URL, 1024); err == nil || !strings.Contains(err.Error(), "HTTPS required") {
		t.Fatalf("expected insecure redirect rejection, got %v", err)
	}
}

func TestUpgradeRedirectRejectsHTTPSDowngradeToLoopback(t *testing.T) {
	source, _ := url.Parse("https://downloads.example/artifact")
	destination, _ := url.Parse("http://127.0.0.1/artifact")
	req := &http.Request{URL: destination}
	via := []*http.Request{{URL: source}}
	if err := validateUpgradeRedirect(req, via); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("expected downgrade rejection, got %v", err)
	}
}

func TestChecksumForRejectsMalformedDigest(t *testing.T) {
	if got := checksumFor([]byte("not-a-sha  keydris-linux-amd64\n"), "keydris-linux-amd64"); got != "" {
		t.Fatalf("accepted malformed checksum %q", got)
	}
}

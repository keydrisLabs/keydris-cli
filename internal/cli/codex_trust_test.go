package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
)

// Optional compatibility check against a locally installed native Codex. It
// uses an isolated home, no credentials, and only a loopback TLS endpoint.
// The endpoint deliberately rejects inference: no real model call is made.
func TestCodexNativeTrustLoopback(t *testing.T) {
	binary := os.Getenv("KEYDRIS_TEST_CODEX_BINARY")
	if binary == "" {
		t.Skip("set KEYDRIS_TEST_CODEX_BINARY to test the installed native client")
	}
	for _, websocket := range []bool{false, true} {
		t.Run(fmt.Sprintf("websocket=%t", websocket), func(t *testing.T) {
			testCodexNativeTrustLoopback(t, binary, websocket)
		})
	}
}

func testCodexNativeTrustLoopback(t *testing.T, binary string, websocket bool) {
	t.Helper()
	root, err := proxy.GenerateCA("Keydris loopback test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := root.LeafFor("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r.Method + " " + r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"keydris loopback trust probe complete","type":"invalid_request_error"}}`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "loopback.pem")
	if err := os.WriteFile(ca, root.CertPEM(), 0600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE", "COMSPEC", "PATHEXT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "CODEX_HOME="+dir)
	env, cleanup, err := codexTrustEnvironment(env, ca, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	provider := fmt.Sprintf(`model_providers.keydris_test={name="Keydris loopback",base_url=%q,wire_api="responses",requires_openai_auth=false,request_max_retries=0,stream_max_retries=0,supports_websockets=%t}`, server.URL+"/v1", websocket)
	cmd := exec.CommandContext(ctx, binary, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "--sandbox", "read-only",
		"-c", `model_provider="keydris_test"`, "-c", `model="gpt-5.4"`, "-c", provider, "Respond with OK without using tools.")
	cmd.Env, cmd.Dir = env, dir
	output, _ := cmd.CombinedOutput() // The intentional HTTP 400 ends the run.
	select {
	case request := <-requests:
		method := "POST"
		if websocket {
			method = "GET"
		}
		if request != method+" /v1/responses" {
			t.Fatalf("unexpected request: %s", request)
		}
	default:
		t.Fatalf("native Codex did not trust the loopback CA: %s", output)
	}
}

func TestCodexTrustPreservesExistingRootsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	keydris := filepath.Join(dir, "keydris.pem")
	custom := filepath.Join(dir, "custom.pem")
	for _, path := range []string{keydris, custom} {
		if _, err := proxy.LoadOrCreateCA(path, path+".key", filepath.Base(path), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	original := []string{"KEEP=unchanged", "codex_ca_certificate=" + custom, "SSL_CERT_FILE=unused.pem"}
	env, cleanup, err := codexTrustEnvironment(original, keydris, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var merged string
	count := 0
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "CODEX_CA_CERTIFICATE") {
			merged = value
			count++
		}
	}
	if count != 1 || merged == custom || merged == keydris {
		t.Fatalf("invalid CA environment: %v", env)
	}
	body, err := os.ReadFile(merged)
	if err != nil {
		t.Fatal(err)
	}
	certs := 0
	for len(body) > 0 {
		block, rest := pem.Decode(body)
		if block == nil {
			break
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			t.Fatal(err)
		}
		certs++
		body = rest
	}
	if certs != 2 {
		t.Fatalf("got %d certificates", certs)
	}
	if original[1] != "codex_ca_certificate="+custom || !strings.Contains(strings.Join(env, "\n"), "KEEP=unchanged") {
		t.Fatal("caller environment mutated")
	}
	cleanup()
	if _, err := os.Stat(merged); !os.IsNotExist(err) {
		t.Fatal("temporary trust file remains")
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal("custom trust file was removed")
	}
}

func TestCodexTrustRejectsBrokenCustomTrustAndFallsBackToSSL(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if _, err := proxy.LoadOrCreateCA(ca, ca+".key", "test", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := codexTrustEnvironment([]string{"CODEX_CA_CERTIFICATE=" + filepath.Join(dir, "missing.pem")}, ca, dir); err == nil {
		t.Fatal("missing custom trust silently discarded")
	}
	env, cleanup, err := codexTrustEnvironment([]string{"SSL_CERT_FILE=" + ca}, ca, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(strings.Join(env, "\n"), "CODEX_CA_CERTIFICATE=") {
		t.Fatal("Codex trust not configured")
	}
}

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/login"
)

func TestAccessTokenLoginRejectsInvalidStdinBeforeEnrollment(t *testing.T) {
	cfg := uxConfig(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg.ControlURL = server.URL
	for _, input := range []io.Reader{
		strings.NewReader(""),
		strings.NewReader(" \n"),
		strings.NewReader("first\nsecond"),
		strings.NewReader(strings.Repeat("x", 64*1024+1)),
	} {
		if accessTokenLogin(cfg, input) == 0 {
			t.Fatal("invalid stdin was accepted")
		}
	}
	if requests != 0 {
		t.Fatal("invalid stdin was sent to the enrollment endpoint")
	}
}

// TestRunLoginRejectsUnexpectedArguments covers the non-flag cases the CLI must
// refuse before it loads config or touches the network.
func TestRunLoginRejectsUnexpectedArguments(t *testing.T) {
	uxConfig(t)
	for _, args := range [][]string{
		{"unexpected"},
		{"--unknown"},
		{"--access-token-stdin", "--no-browser"},
	} {
		if code := runLogin(args); code != 1 {
			t.Errorf("runLogin(%q) = %d, want 1", args, code)
		}
	}
}

// withStdin replaces os.Stdin for one test so entry points that read the
// enrollment token can be driven without touching the real console.
func withStdin(t *testing.T, input string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = old
		reader.Close()
	})
	if _, err := io.WriteString(writer, input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	return string(out)
}

// TestRunLoginAccessTokenStdin drives the non-browser login path end to end:
// the token is read from stdin, presented to the control plane, and the
// returned identity is stored and reported.
func TestRunLoginAccessTokenStdin(t *testing.T) {
	cfg := uxConfig(t)
	const token = "test-cognito-access-token"
	const agentID = "11111111-1111-4111-8111-111111111111"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/identity/sign" {
			t.Errorf("unexpected enrollment request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization = %q, want the stdin bearer token", got)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificate": "certificate-pem",
			"ca_cert":     "ca-pem",
			"spiffe_id":   "spiffe://keydris.local/agent/test/" + agentID,
			"subject":     "ci@example.com",
			"email":       "ci@example.com",
			"not_after":   "2030-01-01T00:00:00Z",
			"device_id":   "device-123",
			"agent_id":    agentID,
		})
	}))
	defer server.Close()
	t.Setenv("KEYDRIS_CONTROL_URL", server.URL)
	t.Setenv("KEYDRIS_AGENT_ID", agentID)
	withStdin(t, "  "+token+"\n")

	var code int
	output := captureStdout(t, func() {
		code = runLogin([]string{"--access-token-stdin"})
	})
	if code != 0 {
		t.Fatalf("runLogin --access-token-stdin exited %d", code)
	}
	if requests != 1 {
		t.Fatalf("enrollment requests = %d, want 1", requests)
	}
	for _, want := range []string{"ci@example.com", agentID, "device-123", cfg.IdentityDir} {
		if !strings.Contains(output, want) {
			t.Fatalf("login output %q missing %q", output, want)
		}
	}
	for _, name := range []string{login.KeyFile, login.CertFile, login.CAFile, login.WhoamiFile} {
		body, err := os.ReadFile(filepath.Join(cfg.IdentityDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), token) {
			t.Fatalf("%s persisted the access token", name)
		}
	}
}

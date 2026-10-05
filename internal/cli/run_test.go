package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestAppendProxyEnvironmentPointsCodexAtTheKeydrisCA(t *testing.T) {
	env := appendProxyEnvironment(nil, "http://keydris:handle@127.0.0.1:15001", "/keydris/ca-bundle.crt")

	for _, want := range []string{
		"HTTPS_PROXY=http://keydris:handle@127.0.0.1:15001",
		"NODE_EXTRA_CA_CERTS=/keydris/ca-bundle.crt",
		// Codex adds this bundle to the native roots. On Windows it is the only
		// way Codex trusts the Keydris CA without `init --trust-store`.
		"CODEX_CA_CERTIFICATE=/keydris/ca-bundle.crt",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment is missing %q: %v", want, env)
		}
	}

	replacesRoots := slices.Contains(env, "SSL_CERT_FILE=/keydris/ca-bundle.crt")
	if runtime.GOOS == "windows" && replacesRoots {
		t.Errorf("SSL_CERT_FILE must not replace the Windows root store: %v", env)
	}
	if runtime.GOOS != "windows" && !replacesRoots {
		t.Errorf("SSL_CERT_FILE is missing: %v", env)
	}
}

// appendProxyEnvironment feeds every toolchain the wrapper can launch, so it
// must set all the proxy spellings and CA variables the current platform's
// tools read. It also must extend, not replace, the caller's environment and
// must not emit duplicate keys whose winning value is platform-dependent.
func TestAppendProxyEnvironmentSetsTheCompletePlatformContract(t *testing.T) {
	const (
		proxy = "http://keydris:handle@127.0.0.1:15001"
		ca    = "/keydris/ca-bundle.crt"
	)
	parent := []string{"KEEP_ME=1", "PATH=/usr/bin:/bin"}
	env := appendProxyEnvironment(append([]string(nil), parent...), proxy, ca)

	for _, want := range []string{
		"HTTP_PROXY=" + proxy,
		"HTTPS_PROXY=" + proxy,
		"http_proxy=" + proxy,
		"https_proxy=" + proxy,
		// Codex reads its own additive variable before SSL_CERT_FILE.
		"NODE_EXTRA_CA_CERTS=" + ca,
		"CODEX_CA_CERTIFICATE=" + ca,
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment is missing %q: %v", want, env)
		}
	}

	// The replacement-style variables would hide the Windows root store, so the
	// Windows branch must leave them unset; the portable branch sets all four.
	for _, key := range []string{"CURL_CA_BUNDLE", "SSL_CERT_FILE", "GIT_SSL_CAINFO", "REQUESTS_CA_BUNDLE"} {
		want := key + "=" + ca
		present := slices.Contains(env, want)
		if runtime.GOOS == "windows" && present {
			t.Errorf("%s must not replace the Windows root store: %v", key, env)
		}
		if runtime.GOOS != "windows" && !present {
			t.Errorf("environment is missing %q: %v", want, env)
		}
	}

	for _, kept := range parent {
		if !slices.Contains(env, kept) {
			t.Errorf("caller entry %q was dropped: %v", kept, env)
		}
	}
	seen := make(map[string]bool, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			t.Errorf("malformed environment entry %q", entry)
			continue
		}
		if seen[key] {
			t.Errorf("duplicate environment key %q: %v", key, env)
		}
		seen[key] = true
	}
}

// runRun is the path that actually exports the CA to a governed Codex process:
// the helper can be correct while the wrapper never calls it. Capture the
// child's environment to prove CODEX_CA_CERTIFICATE is replaced with the
// Keydris bundle rather than inherited from the parent.
func TestRunExportsTheKeydrisCACertificateToTheWrappedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake env-capture shim is a POSIX script")
	}
	dir := t.TempDir()
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv("KEYDRIS_AGENT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("KEYDRIS_SESSION_SOCKET", filepath.Join(dir, "missing.sock"))
	// A stale parent value proves the wrapper overrides it; os/exec keeps the
	// last duplicate, which is the value appendProxyEnvironment appends.
	t.Setenv("CODEX_CA_CERTIFICATE", "/parent/sentinel")
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mints, revokes int
	oldMint, oldRevoke, oldSend, oldExchange, oldRoutes := mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes
	mintSessionInstance = func(*config.Config, string, string, string) (*mintedInstance, error) {
		mints++
		return &mintedInstance{SPIFFEID: "spiffe://keydris.test/run", KIT: "test-kit", SessionID: "test-ulid"}, nil
	}
	revokeSessionInstance = func(*config.Config, string) error { revokes++; return nil }
	sendSessionMessage = func(string, sessionsock.Message) error { return nil }
	exchangeSessionMessage = func(string, sessionsock.Message) (*sessionsock.SessionSnapshot, error) { return nil, nil }
	fetchSessionRoutes = func(cfg *config.Config, _ string) (*runtimecontract.RuntimeRoutes, error) {
		return testSessionRoutes(cfg.AgentID), nil
	}
	defer func() {
		mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes = oldMint, oldRevoke, oldSend, oldExchange, oldRoutes
	}()

	captured := filepath.Join(dir, "child-env.txt")
	script := filepath.Join(dir, "capture-env")
	body := `#!/bin/sh
printf '%s\n' "$CODEX_CA_CERTIFICATE" "$NODE_EXTRA_CA_CERTS" "$HTTPS_PROXY" > "$1"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runRun([]string{"--", script, captured}); code != 0 {
		t.Fatalf("run code = %d", code)
	}
	if mints != 1 || revokes != 1 {
		t.Fatalf("mints=%d revokes=%d, want one each", mints, revokes)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(got) != 3 {
		t.Fatalf("captured %d child values: %q", len(got), raw)
	}
	bundle := filepath.Join(dir, "ca-bundle.crt")
	if got[0] != bundle {
		t.Errorf("child CODEX_CA_CERTIFICATE = %q, want %q", got[0], bundle)
	}
	if got[1] != bundle {
		t.Errorf("child NODE_EXTRA_CA_CERTS = %q, want %q", got[1], bundle)
	}
	if !strings.HasPrefix(got[2], "http://keydris:") || !strings.Contains(got[2], "@127.0.0.1:") {
		t.Errorf("child HTTPS_PROXY = %q, want an authenticated loopback proxy", got[2])
	}
}

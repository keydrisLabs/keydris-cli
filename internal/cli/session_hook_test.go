package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
)

func TestProxyAuthURL(t *testing.T) {
	if got := proxyAuthURL(15001, ""); got != "http://127.0.0.1:15001" {
		t.Errorf("no token: got %q", got)
	}
	if got := proxyAuthURL(15001, "abc123"); got != "http://keydris:abc123@127.0.0.1:15001" {
		t.Errorf("with token: got %q", got)
	}
}

func TestNewProxyTokenUnique(t *testing.T) {
	a, b := newProxyToken(), newProxyToken()
	if a == "" || a == b {
		t.Errorf("tokens should be non-empty and unique, got %q and %q", a, b)
	}
}

// TestWriteClaudeProxyEnvPerSession is the concurrent-isolation guarantee: two
// sessions each get their own token written into their own $CLAUDE_ENV_FILE, so
// their egress carries distinct Proxy-Authorization values.
func TestWriteClaudeProxyEnvPerSession(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001, DataPlane: "sandbox"}

	for _, tc := range []struct{ sid, token string }{
		{"sess-A", "tokenAAAA"},
		{"sess-B", "tokenBBBB"},
	} {
		if err := saveState(cfg, sessionState{SessionID: tc.sid, Handle: tc.token}); err != nil {
			t.Fatal(err)
		}
		envFile := filepath.Join(dir, tc.sid+".env")
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		writeClaudeProxyEnv(cfg, tc.sid)

		b, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatalf("env file for %s: %v", tc.sid, err)
		}
		want := "export HTTPS_PROXY=http://keydris:" + tc.token + "@127.0.0.1:15001"
		if !strings.Contains(string(b), want) {
			t.Errorf("%s: env file missing %q; got:\n%s", tc.sid, want, b)
		}
	}
}

func TestWriteClaudeProxyEnvNoopWithoutEnvFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001, DataPlane: "sandbox"}
	if err := saveState(cfg, sessionState{SessionID: "s", Handle: "tok"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_ENV_FILE", "") // not a Claude hook context
	writeClaudeProxyEnv(cfg, "s")   // must be a safe no-op
}

func TestWrapperOwnedClaudeHooksReuseSession(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001, DataPlane: "sandbox"}
	const sid = "run-test"
	const token = "outer-token"
	if err := saveState(cfg, sessionState{SessionID: sid, Handle: token, ULID: "outer-ulid"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	envFile := filepath.Join(dir, "claude.env")
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv("KEYDRIS_HTTP_PROXY_PORT", "15001")
	t.Setenv("KEYDRIS_SESSION", sid)
	t.Setenv(sessionOwnerEnv, sessionOwnerRun)
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	if code := runInternalSessionHook("start", nil); code != 0 {
		t.Fatalf("start code = %d", code)
	}
	state, err := loadState(cfg, sid)
	if err != nil {
		t.Fatal(err)
	}
	if state.Handle != token || state.ULID != "outer-ulid" {
		t.Fatalf("wrapper state was replaced: %+v", state)
	}
	if code := runInternalSessionHook("end", nil); code != 0 {
		t.Fatalf("end code = %d", code)
	}
	if _, err := loadState(cfg, sid); err != nil {
		t.Fatalf("hook end removed wrapper-owned state: %v", err)
	}
	body, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), token) {
		t.Fatalf("proxy env missing wrapper token: %s", body)
	}
}

func TestRunOwnsOneMintAndRevoke(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv("KEYDRIS_SESSION_SOCKET", filepath.Join(dir, "missing.sock"))
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mints, revokes int
	oldMint, oldRevoke, oldSend := mintSessionInstance, revokeSessionInstance, sendSessionMessage
	mintSessionInstance = func(*config.Config, string, string) (*mintedInstance, error) {
		mints++
		return &mintedInstance{SPIFFEID: "spiffe://keydris.test/run", SVID: "test-svid", ULID: "test-ulid"}, nil
	}
	revokeSessionInstance = func(*config.Config, string) error {
		revokes++
		return nil
	}
	sendSessionMessage = func(string, sessionsock.Message) error { return nil }
	defer func() {
		mintSessionInstance, revokeSessionInstance, sendSessionMessage = oldMint, oldRevoke, oldSend
	}()

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	if code := runRun([]string{"--", truePath}); code != 0 {
		t.Fatalf("run code = %d", code)
	}
	if mints != 1 || revokes != 1 {
		t.Fatalf("mints=%d revokes=%d, want one each", mints, revokes)
	}
}

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
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

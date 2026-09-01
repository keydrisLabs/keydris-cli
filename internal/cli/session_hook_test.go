package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
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
		want := "export HTTPS_PROXY='http://keydris:" + tc.token + "@127.0.0.1:15001'"
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
	t.Setenv("KEYDRIS_AGENT_ID", "11111111-1111-4111-8111-111111111111")
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
	t.Setenv("KEYDRIS_AGENT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("KEYDRIS_SESSION_SOCKET", filepath.Join(dir, "missing.sock"))
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mints, revokes int
	oldMint, oldRevoke, oldSend, oldExchange, oldRoutes := mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes
	mintSessionInstance = func(*config.Config, string, string) (*mintedInstance, error) {
		mints++
		return &mintedInstance{SPIFFEID: "spiffe://keydris.test/run", KIT: "test-kit", SessionID: "test-ulid"}, nil
	}
	revokeSessionInstance = func(*config.Config, string) error {
		revokes++
		return nil
	}
	sendSessionMessage = func(string, sessionsock.Message) error { return nil }
	exchangeSessionMessage = func(string, sessionsock.Message) (*sessionsock.SessionSnapshot, error) { return nil, nil }
	fetchSessionRoutes = func(cfg *config.Config, _ string) (*runtimecontract.RuntimeRoutes, error) {
		return testSessionRoutes(cfg.AgentID), nil
	}
	defer func() {
		mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes = oldMint, oldRevoke, oldSend, oldExchange, oldRoutes
	}()

	command := []string{"--"}
	if runtime.GOOS == "windows" {
		cmdPath, err := exec.LookPath("cmd")
		if err != nil {
			t.Fatal(err)
		}
		command = append(command, cmdPath, "/c", "exit", "0")
	} else {
		truePath, err := exec.LookPath("true")
		if err != nil {
			t.Fatal(err)
		}
		command = append(command, truePath)
	}
	if code := runRun(command); code != 0 {
		t.Fatalf("run code = %d", code)
	}
	if mints != 1 || revokes != 1 {
		t.Fatalf("mints=%d revokes=%d, want one each", mints, revokes)
	}
}

func TestRepeatedSessionStartRevokesPreviousInstance(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:         dir,
		DataPlane:       "sandbox",
		SessionSocket:   filepath.Join(dir, "registry.sock"),
		SessionAuthFile: filepath.Join(dir, "session.auth"),
	}

	var mints, revokes int
	oldMint, oldRevoke, oldSend, oldExchange, oldRoutes := mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes
	mintSessionInstance = func(*config.Config, string, string) (*mintedInstance, error) {
		mints++
		return &mintedInstance{
			SPIFFEID:  "spiffe://keydris.test/session",
			KIT:       "kit",
			SessionID: "ulid-" + string(rune('0'+mints)),
		}, nil
	}
	revokeSessionInstance = func(*config.Config, string) error {
		revokes++
		return nil
	}
	sendSessionMessage = func(string, sessionsock.Message) error { return nil }
	exchangeSessionMessage = func(string, sessionsock.Message) (*sessionsock.SessionSnapshot, error) { return nil, nil }
	fetchSessionRoutes = func(_ *config.Config, _ string) (*runtimecontract.RuntimeRoutes, error) {
		return testSessionRoutes("policy"), nil
	}
	defer func() {
		mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes = oldMint, oldRevoke, oldSend, oldExchange, oldRoutes
	}()

	if code := hookSessionStart(cfg, "policy", "same-session"); code != 0 {
		t.Fatalf("first start code = %d", code)
	}
	if code := hookSessionStart(cfg, "policy", "same-session"); code != 0 {
		t.Fatalf("second start code = %d", code)
	}
	if mints != 2 || revokes != 1 {
		t.Fatalf("mints=%d revokes=%d, want 2 and 1", mints, revokes)
	}
	state, err := loadState(cfg, "same-session")
	if err != nil {
		t.Fatal(err)
	}
	if state.ULID != "ulid-2" {
		t.Fatalf("state ULID = %q, want ulid-2", state.ULID)
	}
}

func testSessionRoutes(agentID string) *runtimecontract.RuntimeRoutes {
	return &runtimecontract.RuntimeRoutes{
		SchemaVersion:  1,
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		Agent: runtimecontract.RoutesAgent{
			AgentID:     agentID,
			DisplayName: "Test agent",
		},
		Routes: []runtimecontract.RuntimeRoute{},
	}
}

func TestSessionIDRejectsPaths(t *testing.T) {
	for _, value := range []string{"", "..", "../escape", `..\escape`, "has/slash", "has space"} {
		if err := validateSessionID(value); err == nil {
			t.Errorf("validateSessionID(%q) unexpectedly succeeded", value)
		}
	}
	for _, value := range []string{"session-1", "claude.resume_2"} {
		if err := validateSessionID(value); err != nil {
			t.Errorf("validateSessionID(%q): %v", value, err)
		}
	}
}

func TestCodexCommandArgsEnableSandboxedUpstreamProxy(t *testing.T) {
	got := codexCommandArgs([]string{"--model", "example"})
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"features.hooks=true",
		"sandbox_workspace_write.network_access=true",
		"features.network_proxy.enabled=true",
		`features.network_proxy.domains={"*"="allow","127.0.0.1"="allow","localhost"="allow"}`,
		"--model example",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Codex args missing %q: %v", want, got)
		}
	}
}

func TestCodexHookFlagsCannotBeOverridden(t *testing.T) {
	for _, args := range [][]string{
		{"--disable", "hooks"},
		{"--disable=hooks"},
		{"-c", "features.hooks=false"},
		{`-c"features"."hooks"=false`},
		{"--config", "features = { network_proxy = {}, hooks = false }"},
		{"--config=features.codex_hooks=false"},
	} {
		if err := validateCodexHookArgs(args); err == nil {
			t.Errorf("unsafe Codex args were accepted: %v", args)
		}
	}
	if err := validateCodexHookArgs([]string{"--model", "example"}); err != nil {
		t.Fatalf("ordinary Codex args were rejected: %v", err)
	}
}

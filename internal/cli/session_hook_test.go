package cli

import (
	"bytes"
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

func TestDesktopSessionStartAttachesWithoutMint(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001, DataPlane: "sandbox"}
	const sid = "desktop-sess-1"
	const token = "desktop-handle"
	const ulid = "desktop-ulid"
	if err := saveState(cfg, sessionState{SessionID: sid, Handle: token, ULID: ulid}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	envFile := filepath.Join(dir, "claude.env")
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv("KEYDRIS_HTTP_PROXY_PORT", "15001")
	t.Setenv(sessionOwnerEnv, "")
	t.Setenv(desktopSessionEnv, sid)
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	// No control-plane identity: mint would fail if start tried to create a session.

	if code := runInternalSessionHook("start", nil); code != 0 {
		t.Fatalf("start code = %d", code)
	}
	state, err := loadState(cfg, sid)
	if err != nil {
		t.Fatal(err)
	}
	if state.Handle != token || state.ULID != ulid {
		t.Fatalf("desktop state was replaced: %+v", state)
	}
	body, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "export HTTPS_PROXY='http://keydris:" + token + "@127.0.0.1:15001'"
	if !strings.Contains(string(body), want) {
		t.Fatalf("env file missing %q; got:\n%s", want, body)
	}
}

func TestDesktopSessionEndLeavesState(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001, DataPlane: "sandbox"}
	const sid = "desktop-sess-end"
	if err := saveState(cfg, sessionState{SessionID: sid, Handle: "keep-handle", ULID: "keep-ulid"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv(sessionOwnerEnv, "")
	t.Setenv(desktopSessionEnv, sid)

	if code := runInternalSessionHook("end", nil); code != 0 {
		t.Fatalf("end code = %d", code)
	}
	if _, err := loadState(cfg, sid); err != nil {
		t.Fatalf("desktop end removed state: %v", err)
	}
}

func TestNestedRunInsideDesktopKeepsItsOwnSession(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001, DataPlane: "sandbox"}
	for _, st := range []sessionState{
		{SessionID: "desktop-outer", Handle: "desktop-handle", ULID: "desktop-ulid"},
		{SessionID: "run-inner", Handle: "run-handle", ULID: "run-ulid"},
	} {
		if err := saveState(cfg, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	envFile := filepath.Join(dir, "claude.env")
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv("KEYDRIS_HTTP_PROXY_PORT", "15001")
	t.Setenv(desktopSessionEnv, "desktop-outer")
	t.Setenv("KEYDRIS_SESSION", "run-inner")
	t.Setenv(sessionOwnerEnv, sessionOwnerRun)
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	if code := runInternalSessionHook("start", nil); code != 0 {
		t.Fatalf("start code = %d", code)
	}
	body, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "run-handle") || strings.Contains(string(body), "desktop-handle") {
		t.Fatalf("nested run exported the wrong session:\n%s", body)
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
	mintSessionInstance = func(*config.Config, string, string, string) (*mintedInstance, error) {
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
	mintSessionInstance = func(*config.Config, string, string, string) (*mintedInstance, error) {
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

	if code := hookSessionStart(cfg, "policy", "same-session", ""); code != 0 {
		t.Fatalf("first start code = %d", code)
	}
	if code := hookSessionStart(cfg, "policy", "same-session", ""); code != 0 {
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

// The wrapped tool's identity is reported on the mint so the console can
// attribute the session. A fake claude on PATH keeps the mapping honest through
// runRun rather than only unit-testing agentRuntimeForCommand.
func TestRunReportsClaudeRuntimeToMint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude shim is a POSIX script")
	}
	dir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_DATAPLANE", "sandbox")
	t.Setenv("KEYDRIS_AGENT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("KEYDRIS_SESSION_SOCKET", filepath.Join(dir, "missing.sock"))
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mintedRuntime string
	oldMint, oldRevoke, oldSend, oldExchange, oldRoutes := mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes
	mintSessionInstance = func(_ *config.Config, _, _, agentRuntime string) (*mintedInstance, error) {
		mintedRuntime = agentRuntime
		return &mintedInstance{SPIFFEID: "spiffe://keydris.test/run", KIT: "test-kit", SessionID: "test-ulid"}, nil
	}
	revokeSessionInstance = func(*config.Config, string) error { return nil }
	sendSessionMessage = func(string, sessionsock.Message) error { return nil }
	exchangeSessionMessage = func(string, sessionsock.Message) (*sessionsock.SessionSnapshot, error) { return nil, nil }
	fetchSessionRoutes = func(cfg *config.Config, _ string) (*runtimecontract.RuntimeRoutes, error) {
		return testSessionRoutes(cfg.AgentID), nil
	}
	defer func() {
		mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes = oldMint, oldRevoke, oldSend, oldExchange, oldRoutes
	}()

	if code := runRun([]string{"--", "claude"}); code != 0 {
		t.Fatalf("run code = %d", code)
	}
	if mintedRuntime != agentRuntimeClaudeCode {
		t.Fatalf("minted agent_runtime = %q, want %q", mintedRuntime, agentRuntimeClaudeCode)
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
	got := codexCommandArgs([]string{"--model", "example"}, codexEnforcementOverrides())
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

// A -c after the subcommand discards every -c before it, so the governed
// values follow the user's last one.
func TestCodexCommandArgsFollowTheUsersLastConfig(t *testing.T) {
	got := codexCommandArgs([]string{"exec", "-c", `approval_policy="never"`, "--json", "list files"}, []string{"features.hooks=true"})
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, `exec -c approval_policy="never" -c features.hooks=true`) ||
		!strings.HasSuffix(joined, "--json list files") {
		t.Fatalf("arguments = %q", got)
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

func TestCodexWindowsManagedNetworkingUsesElevatedSandbox(t *testing.T) {
	args := strings.Join(codexCommandArgs(nil, codexEnforcementOverrides()), " ")
	if got := strings.Contains(args, `windows.sandbox="elevated"`); got != (runtime.GOOS == "windows") {
		t.Fatalf("platform %s has incorrect sandbox arguments: %s", runtime.GOOS, args)
	}
	for _, config := range []string{
		`windows.sandbox="unelevated"`,
		`"windows"."sandbox" = 'unelevated'`,
		`windows = { sandbox = "unelevated" }`,
	} {
		for _, args := range [][]string{{"-c", config}, {"--config=" + config}, {"-c" + config}} {
			err := validateCodexHookArgs(args)
			if (err != nil) != (runtime.GOOS == "windows") {
				t.Fatalf("platform %s: sandbox override %v returned %v", runtime.GOOS, args, err)
			}
		}
	}
}

// codexWrapperFixture stubs the session calls `keydris codex` makes and puts a
// fake codex on PATH. It returns the config and the number of minted sessions.
func codexWrapperFixture(t *testing.T) (*config.Config, *int) {
	t.Helper()
	uxConfig(t)
	dataDir := t.TempDir()
	t.Setenv("KEYDRIS_DATA_DIR", dataDir)
	t.Setenv("KEYDRIS_AGENT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("KEYDRIS_SESSION_SOCKET", filepath.Join(dataDir, "missing.sock"))
	if err := os.WriteFile(filepath.Join(dataDir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mints := new(int)
	oldMint, oldRevoke, oldSend, oldExchange, oldRoutes := mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes
	mintSessionInstance = func(*config.Config, string, string, string) (*mintedInstance, error) {
		*mints++
		return &mintedInstance{SPIFFEID: "spiffe://keydris.test/codex", KIT: "test-kit", SessionID: "test-ulid"}, nil
	}
	revokeSessionInstance = func(*config.Config, string) error { return nil }
	sendSessionMessage = func(string, sessionsock.Message) error { return nil }
	exchangeSessionMessage = func(string, sessionsock.Message) (*sessionsock.SessionSnapshot, error) {
		return nil, nil
	}
	fetchSessionRoutes = func(cfg *config.Config, _ string) (*runtimecontract.RuntimeRoutes, error) {
		return testSessionRoutes(cfg.AgentID), nil
	}
	t.Cleanup(func() {
		mintSessionInstance, revokeSessionInstance, sendSessionMessage, exchangeSessionMessage, fetchSessionRoutes = oldMint, oldRevoke, oldSend, oldExchange, oldRoutes
	})

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatalf("fake codex shim is not on PATH: %v", err)
	}
	return config.Load(), mints
}

// TestRunCodexRefusesToLaunchWhenTheStartupProbeDoesNotDeny covers the gate
// added with the Codex hook probe: hooks that execute without issuing an
// explicit denial must stop the wrapper before it mints a session or launches
// Codex.
func TestRunCodexRefusesToLaunchWhenTheStartupProbeDoesNotDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex shim is a POSIX script")
	}
	cfg, mints := codexWrapperFixture(t)
	if _, err := configureCodex(cfg); err != nil {
		t.Fatal(err)
	}
	// Run the real hook commands, but have them exit without a verdict.
	t.Setenv("KEYDRIS_TEST_CODEX_HOOK_PROCESS", "1")
	t.Setenv("KEYDRIS_TEST_CODEX_HOOK_SILENT", "1")
	if code := runCodex(nil); code != 1 {
		t.Fatalf("runCodex code = %d, want 1 when the startup probe does not deny", code)
	}
	if *mints != 0 {
		t.Fatalf("runCodex minted %d session(s) despite the failed probe", *mints)
	}
}

// Codex skips an untrusted hook without an error, so a launch whose codex does
// not report every Keydris hook trusted and enabled must not start.
func TestRunCodexRefusesWhenCodexDoesNotTrustTheHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex shim is a POSIX script")
	}
	cfg, mints := codexWrapperFixture(t)
	if _, err := configureCodex(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEYDRIS_TEST_CODEX_HOOK_PROCESS", "1")
	previous := codexSessionFlagHooks
	t.Cleanup(func() { codexSessionFlagHooks = previous })
	var listings int
	codexSessionFlagHooks = func(string, []string) ([]codexListedHook, error) {
		listings++
		var hooks []codexListedHook
		for _, event := range []string{"preToolUse", "permissionRequest", "sessionStart"} {
			hooks = append(hooks, codexListedHook{Key: "/<session-flags>/config.toml:" + event + ":0:0", EventName: event, Source: "sessionFlags", CurrentHash: "sha256:" + event, TrustStatus: "untrusted", Enabled: true})
		}
		return hooks, nil
	}
	if code := runCodex(nil); code != 1 {
		t.Fatalf("runCodex code = %d, want 1 when Codex does not trust the hooks", code)
	}
	if listings != 2 || *mints != 0 {
		t.Fatalf("listings = %d, sessions minted = %d", listings, *mints)
	}
}

// Trusted hooks.json entries from an earlier release would run beside the
// per-launch hooks. The wrapper leaves the file alone and asks for init.
func TestRunCodexRefusesHooksFromAnEarlierRelease(t *testing.T) {
	cfg, mints := codexWrapperFixture(t)
	if _, err := configureCodex(cfg); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"hooks":{"PreToolUse":[{"matcher":"^Bash$","hooks":[{"type":"command","command":"'/usr/local/bin/keydris' __pretool-use --codex","timeout":30}]}]}}`)
	if err := os.MkdirAll(filepath.Dir(cfg.CodexHooksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CodexHooksPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCodex(nil); code != 1 {
		t.Fatalf("runCodex code = %d, want 1 with hooks from an earlier release", code)
	}
	if raw, err := os.ReadFile(cfg.CodexHooksPath); err != nil || !bytes.Equal(raw, legacy) {
		t.Fatalf("hooks.json changed: %s, %v", raw, err)
	}
	if *mints != 0 {
		t.Fatalf("runCodex minted %d session(s)", *mints)
	}
}

func TestRunCodexRequiresInit(t *testing.T) {
	_, mints := codexWrapperFixture(t)
	if code := runCodex(nil); code != 1 || *mints != 0 {
		t.Fatalf("runCodex code = %d, sessions minted = %d, want 1 and 0 before init", code, *mints)
	}
}

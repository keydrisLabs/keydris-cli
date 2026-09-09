package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
)

func uxConfig(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Do not read the developer's actual configuration or credentials.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "KEYDRIS_") {
			t.Setenv(key, "")
		}
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("KEYDRIS_DATA_DIR", filepath.Join(home, "data with spaces"))
	return config.Load()
}
func TestUXProxyRequiresOwnedReadyDaemon(t *testing.T) {
	cfg := uxConfig(t)
	// AF_UNIX paths have platform length limits; keep this fixture short.
	dir, err := os.MkdirTemp("", "kd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg.SessionSocket = filepath.Join(dir, "r.sock")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg.HTTPProxyPort = listener.Addr().(*net.TCPAddr).Port
	if got := inspectProxy(cfg); got.state != "error" || got.pid != 0 {
		t.Fatalf("foreign listener marked healthy: %+v", got)
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(proxyProcessRecord{PID: os.Getpid(), Identity: identity})
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "proxy.pid"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	secret, err := sessionsock.LoadOrCreateSecret(cfg.SessionAuthFile)
	if err != nil {
		t.Fatal(err)
	}
	server, err := sessionsock.Serve(cfg.SessionSocket, secret, attest.NewSessionRegistry(), func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if got := inspectProxy(cfg); got.state == "ok" {
		t.Fatal("registration socket alone was marked ready")
	}
	server.MarkReady(cfg.HTTPProxyPort, cfg.DataPlane, cfg.AgentID)
	if got := inspectProxy(cfg); got.state != "ok" || got.pid != os.Getpid() {
		t.Fatalf("ready owned proxy not recognized: %+v", got)
	}
	cfg.AgentID = "different-agent"
	if got := inspectProxy(cfg); got.state == "ok" {
		t.Fatal("stale daemon config marked healthy")
	}
}
func TestUXResetPreviewAndExecutionPreserveLogs(t *testing.T) {
	cfg := uxConfig(t)
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.crt", "ca.key", "evidence.jsonl", "proxy.log", "telemetry.json", "managed-destinations.json"} {
		if err := os.WriteFile(filepath.Join(cfg.DataDir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Pick an unused port so reset cannot interact with a real local proxy.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	t.Setenv("KEYDRIS_HTTP_PROXY_PORT", strconv.Itoa(port))
	if code := runReset([]string{"--dry-run", "--all"}); code != 0 {
		t.Fatalf("preview code %d", code)
	}
	if _, err := os.Stat(cfg.CAPath); err != nil {
		t.Fatal("dry run removed CA")
	}
	if code := runReset([]string{"--yes", "--keep-trust"}); code != 0 {
		t.Fatalf("reset code %d", code)
	}
	if _, err := os.Stat(cfg.CAPath); !os.IsNotExist(err) {
		t.Fatal("CA was retained")
	}
	for _, name := range []string{"evidence.jsonl", "proxy.log", "telemetry.json"} {
		if _, err := os.Stat(filepath.Join(cfg.DataDir, name)); err != nil {
			t.Fatalf("%s was removed", name)
		}
	}
	if code := runReset([]string{"--yes", "--all", "--keep-trust"}); code != 0 {
		t.Fatalf("all reset code %d", code)
	}
	if _, err := os.Stat(cfg.LedgerPath); !os.IsNotExist(err) {
		t.Fatal("--all kept evidence")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "telemetry.json")); err != nil {
		t.Fatal("privacy preference removed")
	}
}
func TestUXResetRefusesDangerousRootsAndSymlinks(t *testing.T) {
	cfg := uxConfig(t)
	home, _ := os.UserHomeDir()
	for _, root := range []string{home, filepath.Dir(home), filepath.VolumeName(home) + string(filepath.Separator), "relative"} {
		if err := validateResetRoot(root); err == nil {
			t.Fatalf("accepted unsafe root %s", root)
		}
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(cfg.DataDir, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if _, err := planReset(cfg, true); err == nil {
		t.Fatal("reset followed a symlink")
	}
}
func TestUXPlainOutputAndStatusJSON(t *testing.T) {
	cfg := uxConfig(t)
	old := colorMode
	defer func() { colorMode = old }()
	colorMode = "auto"
	var output bytes.Buffer
	newUI(&output).row("ok", "Proxy", "Ready\x1b[31m\nbad")
	if strings.Contains(output.String(), "\x1b") {
		t.Fatal("redirected output contains ANSI")
	}
	colorMode = "always"
	report := collectStatus(cfg, "codex", true, false)
	if report.Ready {
		t.Fatal("fresh install reported ready")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("\\u001b")) || bytes.Contains(raw, []byte("client.key")) {
		t.Fatal("status JSON contains terminal escapes or private key details")
	}
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatal("read-only status created runtime state")
	}
}

func TestUXResetRefusesNewDirectoryContents(t *testing.T) {
	cfg := uxConfig(t)
	dir := filepath.Join(cfg.DataDir, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	targets, err := planReset(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.path == dir {
			if err := removeResetTarget(target); err == nil {
				t.Fatal("new file was swept into old deletion plan")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "new.json")); err != nil {
		t.Fatal("new file was removed")
	}
}

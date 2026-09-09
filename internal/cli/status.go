package cli

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

type healthCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Next   string `json:"next,omitempty"`
}
type statusReport struct {
	Ready  bool              `json:"ready"`
	Checks []healthCheck     `json:"checks"`
	Paths  map[string]string `json:"paths,omitempty"`
}
type proxyHealth struct {
	state, detail string
	pid, sessions int
}

// Reading existing authentication material never enrolls, renews or mints a session.
func inspectProxy(cfg *config.Config) proxyHealth {
	result := proxyHealth{state: "inactive", detail: "Stopped"}
	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "proxy.pid"))
	if err == nil {
		var record proxyProcessRecord
		if json.Unmarshal(raw, &record) != nil || record.PID <= 0 || record.Identity == "" {
			return proxyHealth{state: "error", detail: "Unverifiable PID file; inspect it before restarting"}
		}
		identity, identityErr := processIdentity(record.PID)
		if identityErr != nil && !errors.Is(identityErr, errProcessNotRunning) {
			return proxyHealth{state: "error", detail: "Cannot verify the saved proxy process; check process access permissions"}
		}
		if identityErr == nil {
			if identity != record.Identity {
				return proxyHealth{state: "error", detail: "PID belongs to another process; inspect the stale PID file"}
			}
			result.pid = record.PID
			result.state, result.detail = "error", "Process is running, but its local health check failed; run keydris proxy restart"
			secret, secretErr := os.ReadFile(cfg.SessionAuthFile)
			if secretErr == nil {
				health, healthErr := sessionsock.Inspect(cfg.SessionSocket, strings.TrimSpace(string(secret)))
				if healthErr == nil && health.PID == record.PID && health.Ready && health.Port == proxyListenPort(cfg) && health.DataPlane == cfg.DataPlane && health.AgentID == cfg.AgentID {
					if conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyListenPort(cfg)), 200*time.Millisecond); dialErr == nil {
						conn.Close()
						result.state, result.sessions = "ok", health.Sessions
						result.detail = fmt.Sprintf("Running on port %d (PID %d, %d sessions)", proxyListenPort(cfg), record.PID, health.Sessions)
					}
				}
			}
			return result
		}
	} else if !os.IsNotExist(err) {
		return proxyHealth{state: "error", detail: "Cannot read the proxy PID file"}
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyListenPort(cfg)), 200*time.Millisecond); err == nil {
		conn.Close()
		result.state, result.detail = "error", fmt.Sprintf("Port %d is occupied by an unverified process", proxyListenPort(cfg))
	}
	return result
}

func identityReady(cfg *config.Config) (string, error) {
	id, err := login.Load(cfg.IdentityDir)
	if err != nil {
		return "", err
	}
	if cfg.AgentID == "" || id.AgentID != cfg.AgentID {
		return "", fmt.Errorf("identity is not bound to the configured agent")
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(cfg.IdentityDir, login.CertFile), filepath.Join(cfg.IdentityDir, login.KeyFile))
	if err != nil {
		return "", fmt.Errorf("client certificate/key is missing, invalid, or mismatched")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("invalid client certificate")
	}
	if time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return "", fmt.Errorf("client certificate is outside its validity period")
	}
	return "Certificate valid until " + cert.NotAfter.Local().Format("02 Jan 2006 15:04 MST"), nil
}

func collectStatus(cfg *config.Config, target string, offline, verbose bool) statusReport {
	report := statusReport{Ready: true, Checks: []healthCheck{}}
	add := func(name, state, detail, next string) {
		report.Checks = append(report.Checks, healthCheck{name, state, terminalText(detail), next})
		if state == "error" || state == "warning" {
			report.Ready = false
		}
	}
	for _, check := range environmentChecks("") {
		add(check.Name, check.State, check.Detail, check.Next)
	}
	if err := cfg.ValidatePaths(); err != nil {
		add("Paths", "error", err.Error(), "Use absolute paths in KEYDRIS_* path overrides")
		return report
	}
	if cfg.AgentID == "" {
		add("Agent", "error", "No agent configured", "keydris init")
	} else {
		add("Agent", "ok", cfg.AgentID, "")
	}
	if detail, err := identityReady(cfg); err != nil {
		add("Identity", "error", err.Error(), "keydris login")
	} else {
		add("Identity", "ok", detail, "")
	}
	if pair, err := tls.LoadX509KeyPair(cfg.CAPath, cfg.CAKeyPath); err != nil {
		add("Proxy CA", "error", "Missing or invalid CA certificate/key", "keydris init")
	} else if cert, err := x509.ParseCertificate(pair.Certificate[0]); err != nil || !cert.IsCA || time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		add("Proxy CA", "error", "CA certificate is invalid or expired", "keydris reset --dry-run")
	} else if !bundleContainsCA(cfg.CABundlePath, cert) {
		add("Proxy CA", "error", "CA bundle is missing, invalid, or does not contain the proxy CA", "keydris init")
	} else {
		add("Proxy CA", "ok", "Certificate and trust bundle available", "")
	}
	health := inspectProxy(cfg)
	state, next := health.state, ""
	if state != "ok" {
		if state == "inactive" {
			state = "warning"
		}
		next = "keydris proxy up"
	}
	add("Proxy", state, health.detail, next)
	configured := 0
	for _, integration := range []struct{ name, path, command string }{{"claude-code", cfg.ClaudeSettingsPath, "claude"}, {"codex", cfg.CodexHooksPath, "codex"}} {
		if target != "" && target != integration.name {
			continue
		}
		present, err := sandbox.HasKeydrisHooks(integration.path)
		if err != nil {
			add(integration.name, "error", "Cannot read integration settings", "keydris init "+integration.name)
			continue
		}
		if !present && target == "" {
			add(integration.name, "inactive", "Not configured", "")
			continue
		}
		configured++
		valid := false
		detail := "Configuration needs attention"
		if integration.name == "claude-code" {
			opt, optErr := claudeHookOptions(cfg, true)
			st, verifyErr := sandbox.Verify(integration.path, cfg.HTTPProxyPort, opt)
			if optErr != nil {
				verifyErr = optErr
			}
			valid = verifyErr == nil && st.OK()
			if !valid && len(st.Warnings) > 0 {
				detail = strings.Join(st.Warnings, "; ")
			}
		} else {
			opt, optErr := codexHookOptions()
			if optErr == nil {
				valid, err = sandbox.VerifyCodexHooks(integration.path, opt)
				valid = valid && err == nil
			}
		}
		if !valid {
			add(integration.name, "error", detail, "keydris init "+integration.name)
			continue
		}
		if _, err := hostenv.ResolveCommand(integration.command); err != nil {
			add(integration.name, "warning", "Hooks configured; "+err.Error(), "Install "+integration.command+" for this runtime or correct PATH")
			continue
		}
		detail = "Sandbox and command hooks configured"
		if integration.name == "codex" {
			detail = "Command hooks configured; launch with keydris codex (trust via /hooks once)"
		}
		add(integration.name, "ok", detail, "")
		path, pathErr := agentSkillPath(cfg, integration.name)
		if pathErr != nil {
			add("Agent skill", "warning", pathErr.Error(), "keydris init "+integration.name)
		} else if raw, err := readAgentSkill(path); err != nil {
			add("Agent skill", "warning", "Cannot read "+path+"; bundled guidance is available via keydris skill", "keydris init "+integration.name)
		} else if !bytes.Equal(raw, bundledSkill()) {
			add("Agent skill", "warning", "Older or user-edited skill at "+path+"; local edits are preserved", "Review the skill, then run keydris init "+integration.name)
		} else {
			add("Agent skill", "ok", integration.name+": bundled skill installed", "")
		}
		if integration.name == "claude-code" {
			for _, check := range environmentChecks("claude-code") {
				if check.Name == "Sandbox" {
					add(check.Name, check.State, check.Detail, check.Next)
				}
			}
			for _, path := range claudeProjectSettings() {
				if !sameAbsolutePath(path, cfg.ClaudeSettingsPath) {
					add("Project settings", "warning", "May override global sandbox: "+path, "Review "+path)
				}
			}
		}
	}
	if configured == 0 {
		add("Integration", "warning", "No selected integration is configured", "keydris init")
	}
	if cfg.ManagedScopeError != nil {
		add("Policy scope", "error", cfg.ManagedScopeError.Error(), "keydris init")
	} else if cfg.ManagedMode == proxyscope.ModeSelected {
		add("Policy scope", "ok", fmt.Sprintf("%d cached origins; refreshed at session start", len(cfg.ManagedDestinations)), "")
	} else {
		add("Policy scope", "warning", "Policy scope has not been detected", "keydris init")
	}
	if offline {
		add("Control plane", "inactive", "Not checked (--offline)", "")
	} else {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get(strings.TrimRight(cfg.ControlURL, "/") + "/agent/jwks")
		if err != nil {
			add("Control plane", "warning", "Unreachable", "Check your connection and control URL")
		} else {
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				add("Control plane", "ok", "Reachable", "")
			} else {
				add("Control plane", "warning", fmt.Sprintf("JWKS returned HTTP %d", response.StatusCode), "Check your control URL")
			}
		}
	}
	if verbose {
		report.Paths = map[string]string{"data": cfg.DataDir, "identity": cfg.IdentityDir, "ca": cfg.CAPath, "bundle": cfg.CABundlePath, "claude": cfg.ClaudeSettingsPath, "codex": cfg.CodexHooksPath, "proxy_log": filepath.Join(cfg.DataDir, "proxy.log")}
		for _, integration := range []string{"claude-code", "codex"} {
			if path, err := agentSkillPath(cfg, integration); err == nil {
				report.Paths[integration+"_skill"] = path
			}
		}
	}
	return report
}

func runStatus(args ...string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON")
	verbose := fs.Bool("verbose", false, "include resolved configuration paths")
	offline := fs.Bool("offline", false, "skip the control-plane connectivity check")
	target := fs.String("target", "", "check claude-code or codex only")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *target != "" && *target != "claude-code" && *target != "codex" {
		fmt.Fprintln(os.Stderr, "status: --target must be claude-code or codex")
		return 2
	}
	report := collectStatus(config.Load(), *target, *offline, *verbose)
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return 1
		}
	} else {
		ui := newUI(os.Stdout)
		ui.title("Keydris status")
		for _, check := range report.Checks {
			ui.row(check.State, check.Name, check.Detail)
		}
		if *verbose {
			for _, key := range []string{"data", "identity", "ca", "bundle", "claude", "codex", "claude-code_skill", "codex_skill", "proxy_log"} {
				if value, ok := report.Paths[key]; ok {
					ui.row("inactive", key, value)
				}
			}
		}
		if report.Ready {
			ui.row("ok", "Local readiness", "Checks passed")
		} else {
			for _, check := range report.Checks {
				if check.Next != "" {
					ui.next(check.Next)
					break
				}
			}
		}
	}
	if !report.Ready {
		return 1
	}
	return 0
}

func claudeProjectSettings() []string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	var found []string
	for dir := wd; ; dir = filepath.Dir(dir) {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			path := filepath.Join(dir, ".claude", name)
			if _, err := os.Stat(path); err == nil {
				found = append(found, path)
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return found
}
func sameAbsolutePath(a, b string) bool {
	a, ea := filepath.Abs(a)
	b, eb := filepath.Abs(b)
	return ea == nil && eb == nil && pathEqual(a, b)
}

func bundleContainsCA(path string, cert *x509.Certificate) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, cert.Raw) {
			return true
		}
	}
	return false
}

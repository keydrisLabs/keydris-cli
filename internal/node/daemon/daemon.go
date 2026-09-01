// Package daemon is the long-running node service. In Phase 1 it builds a data
// plane (transparent on Linux, or the cross-platform proxy-env fallback),
// orchestrates broker decisions, and tears down on exit. Later phases add the
// eBPF tracer and a local socket for the session hooks.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/authz"
	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/evidence"
	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/dataplane"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/netfilter"
	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

// preflight fails fast with an actionable message rather than starting a broken
// transparent proxy. Only the Linux transparent plane needs root + iptables.
// Kernel/BTF/cgroup-v2 are checked as warnings: their absence degrades the
// race-free eBPF attribution to the /proc resolver but does not block the proxy.
func preflight() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("transparent data plane requires Linux (running on %s); run inside a Linux VM, or set KEYDRIS_DATAPLANE=proxyenv", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("keydris proxy up (transparent) must run as root for iptables/CAP_NET_ADMIN; try: sudo keydris proxy up")
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		return fmt.Errorf("iptables not found in PATH: %w", err)
	}

	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		log.Printf("preflight: cgroup v2 not detected at /sys/fs/cgroup; session cgroup binding may not work")
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
		log.Printf("preflight: kernel BTF (/sys/kernel/btf/vmlinux) absent; eBPF attribution unavailable, using /proc resolver")
	}
	return nil
}

// Run builds the data plane, installs netfilter rules (transparent mode only),
// orchestrates authorize -> inject/reject per flow, and blocks until interrupted.
func Run(cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The daemon authenticates to the control plane over mTLS with the identity
	// `keydris login` stored. Build it up front so we fail fast (rather than per
	// flow) when the node has not been logged in.
	if _, err := login.EnsureFresh(cfg.IdentityDir, cfg.ControlMTLSURL, cfg.MTLSServerCA, 48*time.Hour); err != nil {
		return fmt.Errorf("renew control-plane mTLS identity: %w", err)
	}
	authClient, err := login.HTTPClient(cfg.IdentityDir, cfg.MTLSServerCA, 5*time.Second)
	if err != nil {
		return fmt.Errorf("control-plane mTLS identity (run `keydris login`): %w", err)
	}

	// The session registry maps a platform handle (cgroup) to a registered
	// per-session SVID; the SessionStart hook populates it over the socket.
	sessions := attest.NewSessionRegistry()
	if cfg.ManagedScopeError != nil {
		return fmt.Errorf("read proxy scope: %w", cfg.ManagedScopeError)
	}
	scope, err := proxyscope.New(cfg.ManagedMode, cfg.ManagedDestinations)
	if err != nil {
		return fmt.Errorf("proxy scope: %w", err)
	}
	ledger, err := evidence.Open(cfg.LedgerPath)
	if err != nil {
		return fmt.Errorf("open evidence ledger: %w", err)
	}

	sessionSecret, err := sessionsock.LoadOrCreateSecret(cfg.SessionAuthFile)
	if err != nil {
		return fmt.Errorf("session socket auth: %w", err)
	}
	sock, err := sessionsock.Serve(cfg.SessionSocket, sessionSecret, sessions, log.Printf)
	if err != nil {
		return fmt.Errorf("session socket: %w", err)
	}
	defer sock.Close()
	log.Printf("session registration socket: %s", cfg.SessionSocket)

	dp, usesNetfilter, err := buildDataPlane(cfg, sessions, scope)
	if err != nil {
		return err
	}
	defer dp.Close()
	router := newRuntimeRouter(authClient, cfg.ControlMTLSURL)
	go runSessionRenewalLoop(ctx, cfg, authClient, sessions, log.Printf)

	if usesNetfilter {
		if err := netfilter.Up(cfg.ProxyPort, cfg.BackendPort, cfg.ProxyUID); err != nil {
			return fmt.Errorf("install iptables rules: %w", err)
		}
		defer func() {
			_ = netfilter.Down(cfg.ProxyPort, cfg.BackendPort, cfg.ProxyUID)
			log.Printf("iptables rules removed")
		}()
		log.Printf("iptables rules installed (dport %d -> proxy :%d, exempt uid %d)",
			cfg.BackendPort, cfg.ProxyPort, cfg.ProxyUID)
	}

	// Close the data plane (and thus end the flow loop) on shutdown.
	go func() {
		<-ctx.Done()
		_ = dp.Close()
	}()

	policy := cfg.PolicyID
	if policy == "" {
		policy = "(none)"
	}
	log.Printf("keydris daemon running (dataplane=%s, policy=%s, scope=%s, control=%s)", cfg.DataPlane, policy, scope.Mode(), cfg.ControlMTLSURL)
	for flow := range dp.Flows() {
		go handleFlow(ctx, cfg, authClient, router, dp, scope, ledger, flow)
	}
	return nil
}

// buildDataPlane selects the interception mode. The bool reports whether the
// daemon must manage iptables rules for this plane.
func buildDataPlane(cfg *config.Config, sessions *attest.SessionRegistry, scope *proxyscope.Scope) (dataplane.DataPlane, bool, error) {
	switch cfg.DataPlane {
	case "sandbox", "claude-code":
		ca, err := proxy.LoadOrCreateCA(cfg.CAPath, cfg.CAKeyPath, "Keydris CA", 825*24*time.Hour)
		if err != nil {
			return nil, false, fmt.Errorf("load Keydris CA: %w", err)
		}
		dp, err := dataplane.NewSandboxProxy(
			fmt.Sprintf("127.0.0.1:%d", cfg.HTTPProxyPort), ca, sessions,
			dataplane.SandboxOptions{
				AllowSoleFallback: cfg.AllowSoleFallback,
				PeerVerify:        dataplane.ParsePeerVerify(cfg.PeerVerify),
				Scope:             scope,
			})
		return dp, false, err
	case "", "transparent", "linux":
		if err := preflight(); err != nil {
			return nil, false, err
		}
		if scope.Mode() == proxyscope.ModeSelected {
			for _, destination := range scope.Destinations() {
				host, _, splitErr := net.SplitHostPort(destination)
				if splitErr != nil || net.ParseIP(host) == nil {
					return nil, false, fmt.Errorf("transparent selected scope requires IP literals, got %q; use the sandbox or proxyenv data plane for hostname scope", destination)
				}
			}
		}
		resolver := attest.NewResolver(sessions)
		dp, err := dataplane.NewTransparent(fmt.Sprintf("0.0.0.0:%d", cfg.ProxyPort), resolver, scope)
		return dp, true, err
	case "proxyenv":
		ca, err := proxy.LoadOrCreateCA(cfg.CAPath, cfg.CAKeyPath, "Keydris CA", 825*24*time.Hour)
		if err != nil {
			return nil, false, fmt.Errorf("load Keydris CA: %w", err)
		}
		dp, err := dataplane.NewSandboxProxy(
			fmt.Sprintf("127.0.0.1:%d", cfg.ProxyPort),
			ca, sessions,
			dataplane.SandboxOptions{
				AllowSoleFallback: cfg.AllowSoleFallback,
				PeerVerify:        dataplane.PeerVerifyOff,
				Scope:             scope,
			})
		return dp, false, err
	default:
		return nil, false, fmt.Errorf("unknown KEYDRIS_DATAPLANE %q (want sandbox|transparent|proxyenv)", cfg.DataPlane)
	}
}

func handleFlow(ctx context.Context, cfg *config.Config, client *http.Client, router *runtimeRouter, dp dataplane.DataPlane, scope *proxyscope.Scope, ledger *evidence.Ledger, flow dataplane.Flow) {
	dst := flow.DstString()
	origin := attributionString(flow)
	if flow.Routes != nil {
		if router.handle(ctx, dp, flow) {
			return
		}
	}
	if !scope.Managed(dst) {
		log.Printf("PASSTHROUGH %s %s", dst, origin)
		if err := dp.PassThrough(flow); err != nil {
			log.Printf("passthrough %s: %v", dst, err)
		}
		return
	}

	authReq := authz.AuthorizeRequest{
		DstAddr:    dst,
		DstHost:    flow.DstHost(),
		SessionID:  flow.SessionID,
		SVID:       flow.SVID,
		PolicyID:   cfg.PolicyID,
		ToolCall:   flow.ToolCall,
		ToolParams: flow.ToolParams,
	}
	if flow.MetadataError != "" {
		reason := "invalid request metadata: " + flow.MetadataError
		denial := &authz.AuthorizeResponse{Decision: authz.DecisionDeny, Reason: reason}
		if auditErr := appendAuthorizeAudit(ledger, authReq, denial, nil, 0); auditErr != nil {
			log.Printf("authorize audit %s: %v", dst, auditErr)
			_ = dp.Reject(flow, "authorization audit unavailable")
			return
		}
		log.Printf("DENY  %s %s tool=%q: %s", dst, origin, flow.ToolCall, reason)
		_ = dp.Reject(flow, reason)
		return
	}
	started := time.Now()
	resp, err := authz.Authorize(ctx, client, cfg.ControlMTLSURL, authReq)
	elapsed := time.Since(started)
	if auditErr := appendAuthorizeAudit(ledger, authReq, resp, err, elapsed); auditErr != nil {
		log.Printf("authorize audit %s: %v", dst, auditErr)
		_ = dp.Reject(flow, "authorization audit unavailable")
		return
	}
	if err != nil {
		params := sanitizeAuthorizeText(toolParamsForLog(flow.ToolParams), authReq, resp)
		log.Printf("broker error for %s tool=%q tool_params=%s: %s", dst, flow.ToolCall, params, sanitizeAuthorizeText(err.Error(), authReq, resp))
		_ = dp.Reject(flow, "broker unavailable")
		return
	}

	if resp.Decision != authz.DecisionAllow {
		reason := sanitizeAuthorizeText(resp.Reason, authReq, resp)
		params := sanitizeAuthorizeText(toolParamsForLog(flow.ToolParams), authReq, resp)
		log.Printf("DENY  %s %s tool=%q tool_params=%s: %s", dst, origin, flow.ToolCall, params, reason)
		_ = dp.Reject(flow, reason)
		return
	}

	var cred dataplane.Credential
	if resp.Inject != nil {
		cred = dataplane.Credential{Type: resp.Inject.Type, Name: resp.Inject.Name, Value: resp.Inject.Value}
	}
	params := sanitizeAuthorizeText(toolParamsForLog(flow.ToolParams), authReq, resp)
	log.Printf("ALLOW %s %s tool=%q tool_params=%s (inject %s)", dst, origin, flow.ToolCall, params, cred.Name)
	if err := dp.Inject(flow, cred); err != nil {
		log.Printf("inject %s: %v", dst, err)
	}
}

// attributionString summarizes the resolved origin of a flow for logging.
func attributionString(flow dataplane.Flow) string {
	session := flow.SessionID
	if session == "" {
		session = "none"
	}
	return fmt.Sprintf("[pid=%d cgroup=%q session=%s]", flow.SrcPID, flow.Cgroup, session)
}

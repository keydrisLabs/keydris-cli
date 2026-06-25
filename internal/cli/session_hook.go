package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
)

// Internal Claude Code hook entrypoints. `keydris init claude-code` wires these
// into settings.json as the SessionStart/SessionEnd hooks so each Claude session
// is bound to a freshly minted per-session SPIFFE SVID (and revoked on end).
//
// They are intentionally undocumented (not shown in `keydris help`): users never
// invoke them — Claude Code does, passing its hook payload on stdin. The command
// strings contain "keydris" so `keydris deinit` reliably strips them.
const (
	internalSessionStartCmd = "keydris __session-start"
	internalSessionEndCmd   = "keydris __session-end"
)

// proxyAuthURL builds the local proxy URL a session points its HTTP(S)_PROXY at.
// When token is non-empty it is carried as the Proxy-Authorization password (the
// username is ignored by the proxy), letting the proxy attribute this session
// and isolate it from any concurrent session.
func proxyAuthURL(port int, token string) string {
	if token == "" {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("http://keydris:%s@127.0.0.1:%d", token, port)
}

// runInternalSessionHook handles `keydris __session-start|__session-end`.
func runInternalSessionHook(phase string, args []string) int {
	fs := flag.NewFlagSet("__session", flag.ContinueOnError)
	blueprint := fs.String("blueprint", "", "blueprint to bind (overrides config/env)")
	session := fs.String("session", "", "session id (default: Claude hook payload / $KEYDRIS_SESSION / generated)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg := config.Load()
	switch phase {
	case "start":
		sid := sessionID(*session, true)
		if code := hookSessionStart(cfg, *blueprint, sid); code != 0 {
			return code
		}
		// Best-effort peer-verification anchor: the process that spawned this hook
		// (the Claude session's process). Reliable on Linux when Claude is the
		// direct parent; see the caveat in docs/attribution.md.
		updateSessionOwner(cfg, sid, os.Getppid())
		// Hand the session its per-session proxy token via $CLAUDE_ENV_FILE so
		// every Bash subprocess routes egress through Keydris carrying the token
		// in Proxy-Authorization. This is what isolates concurrent sessions.
		writeClaudeProxyEnv(cfg, sid)
		return 0
	case "end":
		return hookSessionEnd(cfg, sessionID(*session, false))
	default:
		fmt.Fprintf(os.Stderr, "keydris: unknown internal hook %q\n", phase)
		return 1
	}
}

// writeClaudeProxyEnv appends the session's per-session proxy URL (carrying its
// token) to Claude Code's $CLAUDE_ENV_FILE. Claude Code sources these exports for
// the rest of the session, so every Bash subprocess routes egress through the
// Keydris proxy with this session's token in Proxy-Authorization — the mechanism
// that lets the proxy attribute concurrent sessions independently.
//
// No-op outside a Claude hook (no $CLAUDE_ENV_FILE) or in cgroup/transparent mode
// (which attributes by cgroup, not a token).
//
// Caveat: whether a hook-set HTTP_PROXY composes with the sandbox's own
// httpProxyPort routing is not documented and may vary by Claude Code version;
// both point at the same proxy, so this only adds the credential. See docs.
func writeClaudeProxyEnv(cfg *config.Config, sid string) {
	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" || usesCgroupBinding(cfg) {
		return
	}
	st, err := loadState(cfg, sid)
	if err != nil || st.Handle == "" {
		return
	}
	p := proxyAuthURL(cfg.HTTPProxyPort, st.Handle)
	f, err := os.OpenFile(envFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: write proxy env: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "export HTTP_PROXY=%s\nexport HTTPS_PROXY=%s\nexport http_proxy=%s\nexport https_proxy=%s\n", p, p, p, p)
}

// sessionID resolves the session identifier so SessionStart and SessionEnd agree
// on the same per-session identity. Precedence: explicit flag, then
// $KEYDRIS_SESSION/$CLAUDE_SESSION_ID, then Claude Code's hook payload
// (session_id on stdin), then a generated handle (start) or "default" (end).
func sessionID(flagVal string, generate bool) string {
	for _, v := range []string{flagVal, os.Getenv("KEYDRIS_SESSION"), os.Getenv("CLAUDE_SESSION_ID")} {
		if v != "" {
			return v
		}
	}
	if sid := stdinSessionID(); sid != "" {
		return sid
	}
	if generate {
		return time.Now().UTC().Format("20060102T150405")
	}
	return "default"
}

// stdinSessionID reads Claude Code's hook payload from stdin and returns its
// session_id. Claude Code invokes hooks with a JSON object on stdin
// (`{"session_id","hook_event_name","cwd",...}`); correlating on session_id is
// what lets SessionStart and SessionEnd reference the same minted SVID. It
// returns "" on a TTY or an empty/unparseable payload, so the flag/env/generated
// fallbacks still apply and we never block on a terminal.
func stdinSessionID() string {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil || len(b) == 0 {
		return ""
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return ""
	}
	return payload.SessionID
}

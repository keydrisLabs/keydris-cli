package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/node/login"
	"github.com/nocaplabs/keydris-cli/internal/node/sessionsock"
)

// runHook implements `keydris hook session-start|session-end`, the Claude Code
// integration: it binds the session to a cgroup, mints a per-session SPIFFE
// SVID from the control plane, and registers the handle<->SVID with the daemon
// (SessionEnd revokes and unregisters).
func runHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: keydris hook session-start|session-end [--blueprint B] [--session S]")
		return 1
	}
	sub := args[0]
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	blueprint := fs.String("blueprint", "", "blueprint to bind (overrides config/env)")
	session := fs.String("session", "", "session id (default: $KEYDRIS_SESSION/$CLAUDE_SESSION_ID or generated)")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}

	cfg := config.Load()
	switch sub {
	case "session-start":
		return hookSessionStart(cfg, *blueprint, sessionID(*session, true))
	case "session-end":
		return hookSessionEnd(cfg, sessionID(*session, false))
	default:
		fmt.Fprintf(os.Stderr, "keydris hook: unknown subcommand %q\n", sub)
		return 1
	}
}

// bindSessionForMode chooses the session handle for the active data plane. Only
// the transparent (Linux) plane needs a cgroup; the sandbox and proxy-env planes
// attribute by the registered handle alone, so they use an opaque token and skip
// the privileged cgroup write.
func bindSessionForMode(cfg *config.Config, sid string) (string, error) {
	if usesCgroupBinding(cfg) {
		return bindSession(sid)
	}
	return "/keydris/" + sid, nil
}

func unbindSessionForMode(cfg *config.Config, handle string) {
	if usesCgroupBinding(cfg) {
		_ = unbindSession(handle)
	}
}

func usesCgroupBinding(cfg *config.Config) bool {
	switch cfg.DataPlane {
	case "", "transparent", "linux":
		return true
	default: // sandbox, claude-code, proxyenv
		return false
	}
}

// sessionID resolves the session identifier so SessionStart and SessionEnd
// agree on the same per-session identity. Precedence: explicit flag, then
// $KEYDRIS_SESSION/$CLAUDE_SESSION_ID, then Claude Code's hook payload
// (session_id on stdin). Only when none of those exist do we fall back to a
// generated handle (start) or "default" (end).
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
// (`{"session_id","hook_event_name","cwd",...}`); using session_id is what lets
// SessionStart and SessionEnd correlate to the same minted SVID. It returns ""
// when stdin is an interactive terminal or carries no usable payload (e.g. a
// manual `keydris hook ...` invocation), so the flag/env/generated fallbacks
// still apply and we never block on a TTY.
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

type sessionState struct {
	SessionID string `json:"session_id"`
	Handle    string `json:"handle"`
	ULID      string `json:"ulid"`
	SPIFFEID  string `json:"spiffe_id"`
	Blueprint string `json:"blueprint"`
}

func hookSessionStart(cfg *config.Config, blueprintFlag, sid string) int {
	blueprint := cfg.ResolveBlueprint(blueprintFlag)

	handle, err := bindSessionForMode(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: bind session: %v\n", err)
		return 1
	}

	inst, err := mintInstance(cfg, blueprint, handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: mint: %v\n", err)
		return 1
	}

	if err := sessionsock.Send(cfg.SessionSocket, sessionsock.Message{
		Action:    sessionsock.ActionRegister,
		Handle:    handle,
		SPIFFEID:  inst.SPIFFEID,
		SVID:      inst.SVID,
		Blueprint: blueprint,
		ULID:      inst.ULID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: register with daemon: %v\n", err)
		// Identity was minted; continue so the session still has an SVID.
	}

	if err := saveState(cfg, sessionState{
		SessionID: sid, Handle: handle, ULID: inst.ULID, SPIFFEID: inst.SPIFFEID, Blueprint: blueprint,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: save state: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "keydris: session %s bound to %s\n", sid, inst.SPIFFEID)
	return 0
}

func hookSessionEnd(cfg *config.Config, sid string) int {
	st, err := loadState(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: no state for session %q: %v\n", sid, err)
		return 1
	}

	if err := revokeInstance(cfg, st.ULID); err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: revoke: %v\n", err)
	}
	if err := sessionsock.Send(cfg.SessionSocket, sessionsock.Message{
		Action: sessionsock.ActionUnregister, Handle: st.Handle,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris hook: unregister: %v\n", err)
	}
	unbindSessionForMode(cfg, st.Handle)
	_ = removeState(cfg, sid)

	fmt.Fprintf(os.Stderr, "keydris: session %s ended (%s revoked)\n", sid, st.SPIFFEID)
	return 0
}

// --- control-plane client ---

type mintedInstance struct {
	SPIFFEID  string `json:"spiffe_id"`
	SVID      string `json:"svid"`
	ULID      string `json:"ulid"`
	ExpiresAt string `json:"expires_at"`
}

// mTLSClient builds the control-plane client that presents the login identity,
// returning an actionable error when the node has not run `keydris login`.
func mTLSClient(cfg *config.Config) (*http.Client, error) {
	client, err := login.HTTPClient(cfg.IdentityDir, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("control-plane mTLS identity (run `keydris login`): %w", err)
	}
	return client, nil
}

func mintInstance(cfg *config.Config, blueprint, handle string) (*mintedInstance, error) {
	client, err := mTLSClient(cfg)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"blueprint": blueprint, "session_handle": handle})
	resp, err := client.Post(cfg.ControlMTLSURL+"/authorize/issue", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("control returned %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	var out mintedInstance
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func revokeInstance(cfg *config.Config, ulid string) error {
	client, err := mTLSClient(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.ControlMTLSURL+"/authorize/"+ulid+"/revoke", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke returned %s", resp.Status)
	}
	return nil
}

// --- session state (so SessionEnd can find the minted instance) ---

func stateDir(cfg *config.Config) string { return filepath.Join(cfg.DataDir, "sessions") }

func statePath(cfg *config.Config, sid string) string {
	return filepath.Join(stateDir(cfg), sid+".json")
}

func saveState(cfg *config.Config, st sessionState) error {
	if err := os.MkdirAll(stateDir(cfg), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	return os.WriteFile(statePath(cfg, st.SessionID), b, 0o644)
}

func loadState(cfg *config.Config, sid string) (sessionState, error) {
	var st sessionState
	b, err := os.ReadFile(statePath(cfg, sid))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func removeState(cfg *config.Config, sid string) error {
	return os.Remove(statePath(cfg, sid))
}

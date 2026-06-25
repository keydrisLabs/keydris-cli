package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
)

// The session lifecycle below (mint SVID + bind + register, and the reverse) is
// used internally by `keydris run`. It was previously also exposed as
// `keydris hook session-start|session-end` for Claude Code; that command has
// been removed.

// bindSessionForMode chooses the session handle for the active data plane. The
// transparent (Linux) plane needs a cgroup; the sandbox plane uses a random
// per-session token as the handle, which the agent's requests carry via
// Proxy-Authorization so the proxy attributes each concurrent session
// (sandboxproxy.resolveSession -> registry.Lookup).
func bindSessionForMode(cfg *config.Config, sid string) (string, error) {
	if usesCgroupBinding(cfg) {
		return bindSession(sid)
	}
	return newProxyToken(), nil
}

// newProxyToken returns an unguessable per-session token. It is a bearer
// credential: presenting it to the proxy (via Proxy-Authorization) attributes a
// connection to this session's SVID, so it must be random and kept off the wire
// to anyone but the session.
func newProxyToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

func unbindSessionForMode(cfg *config.Config, handle string) {
	if usesCgroupBinding(cfg) {
		_ = unbindSession(handle)
	}
}

func usesCgroupBinding(cfg *config.Config) bool {
	switch cfg.DataPlane {
	case "transparent", "linux":
		return true
	default: // sandbox (default), claude-code, proxyenv
		return false
	}
}

type sessionState struct {
	SessionID string `json:"session_id"`
	Handle    string `json:"handle"`
	ULID      string `json:"ulid"`
	SPIFFEID  string `json:"spiffe_id"`
	Blueprint string `json:"blueprint"`
	OwnerPID  int    `json:"owner_pid,omitempty"`
}

// updateSessionOwner records the session's root process and re-registers it with
// the daemon so the sandbox proxy can verify that connecting processes belong to
// this session (peer verification). Best-effort: on failure the session stays
// token-only (no peer check). A non-positive pid is ignored.
func updateSessionOwner(cfg *config.Config, sid string, pid int) {
	if pid <= 0 {
		return
	}
	st, err := loadState(cfg, sid)
	if err != nil {
		return
	}
	st.OwnerPID = pid
	_ = saveState(cfg, st)
	_ = sessionsock.Send(cfg.SessionSocket, sessionsock.Message{
		Action:   sessionsock.ActionUpdateOwner,
		Handle:   st.Handle,
		OwnerPID: pid,
	})
}

func hookSessionStart(cfg *config.Config, blueprintFlag, sid string) int {
	blueprint := cfg.ResolveBlueprint(blueprintFlag)

	handle, err := bindSessionForMode(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: bind session: %v\n", err)
		return 1
	}

	inst, err := mintInstance(cfg, blueprint, handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: mint: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "keydris session: register with daemon: %v\n", err)
		// Identity was minted; continue so the session still has an SVID.
	}

	if err := saveState(cfg, sessionState{
		SessionID: sid, Handle: handle, ULID: inst.ULID, SPIFFEID: inst.SPIFFEID, Blueprint: blueprint,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: save state: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "keydris: session %s bound to %s\n", sid, inst.SPIFFEID)
	return 0
}

func hookSessionEnd(cfg *config.Config, sid string) int {
	st, err := loadState(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: no state for session %q: %v\n", sid, err)
		return 1
	}

	if err := revokeInstance(cfg, st.ULID); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: revoke: %v\n", err)
	}
	if err := sessionsock.Send(cfg.SessionSocket, sessionsock.Message{
		Action: sessionsock.ActionUnregister, Handle: st.Handle,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: unregister: %v\n", err)
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

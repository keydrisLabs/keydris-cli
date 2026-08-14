package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionstate"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
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

// Keep the package-local name used throughout the CLI while sharing the
// durable schema with the daemon's renewal loop.
type sessionState = sessionstate.State

// updateSessionOwner records the session's root process and re-registers it with
// the daemon so the sandbox proxy can verify that connecting processes belong to
// this session (peer verification). Best-effort: on failure the session stays
// token-only (no peer check). A non-positive pid is ignored.
func updateSessionOwner(cfg *config.Config, sid string, pid int, managed bool) {
	if pid <= 0 {
		return
	}
	st, err := loadState(cfg, sid)
	if err != nil {
		return
	}
	st.OwnerPID = pid
	st.OwnerManaged = managed
	if managed {
		identity, identityErr := processIdentity(pid)
		if identityErr != nil {
			fmt.Fprintf(os.Stderr, "keydris session: identify owner process: %v\n", identityErr)
		} else {
			st.OwnerIdentity = identity
		}
	} else {
		st.OwnerIdentity = ""
	}
	_ = saveState(cfg, st)
	_ = sendAuthenticatedSessionMessage(cfg, sessionsock.Message{
		Action:        sessionsock.ActionUpdateOwner,
		Handle:        st.Handle,
		OwnerPID:      pid,
		OwnerManaged:  managed,
		OwnerIdentity: st.OwnerIdentity,
	})
}

func hookSessionStart(cfg *config.Config, blueprintFlag, sid string) int {
	if err := validateSessionID(sid); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: %v\n", err)
		return 1
	}

	// SessionStart can fire more than once for one logical session (for example
	// after Claude/Codex compaction). Revoke and remove the old instance before
	// replacing its state so its ULID is never orphaned.
	if _, err := loadState(cfg, sid); err == nil {
		if code := hookSessionEnd(cfg, sid); code != 0 {
			fmt.Fprintf(os.Stderr, "keydris session: cannot replace existing session %q\n", sid)
			return code
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "keydris session: read existing state %q: %v\n", sid, err)
		return 1
	}

	blueprint := cfg.ResolveAgent(blueprintFlag)
	if blueprint == "" {
		fmt.Fprintln(os.Stderr, "keydris session: no agent configured (run `keydris init` or `keydris init <target> <agent-id>`)")
		return 1
	}

	handle, err := bindSessionForMode(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: bind session: %v\n", err)
		return 1
	}

	inst, err := mintSessionInstance(cfg, blueprint, handle)
	if err != nil {
		unbindSessionForMode(cfg, handle)
		fmt.Fprintf(os.Stderr, "keydris session: mint: %v\n", err)
		return 1
	}

	routes, err := fetchSessionRoutes(cfg, inst.KIT)
	if err != nil {
		_ = revokeSessionInstance(cfg, inst.SessionID)
		unbindSessionForMode(cfg, handle)
		fmt.Fprintf(os.Stderr, "keydris session: fetch runtime routes: %v\n", err)
		return 1
	}
	if routes.Agent.AgentID != blueprint {
		_ = revokeSessionInstance(cfg, inst.SessionID)
		unbindSessionForMode(cfg, handle)
		fmt.Fprintln(os.Stderr, "keydris session: runtime routes agent does not match the selected agent")
		return 1
	}

	// Keep the proxy scope synced with policy changes automatically.
	refreshPolicyScope(cfg, routes, os.Stderr)

	if err := saveState(cfg, sessionState{
		SessionID: sid, Handle: handle, ULID: inst.SessionID, SPIFFEID: inst.SPIFFEID, Blueprint: blueprint, ExpiresAt: inst.ExpiresAt, KIT: inst.KIT, Routes: routes,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: save state: %v\n", err)
		_ = revokeSessionInstance(cfg, inst.SessionID)
		unbindSessionForMode(cfg, handle)
		_ = removeState(cfg, sid)
		return 1
	}

	if err := sendAuthenticatedSessionMessage(cfg, sessionsock.Message{
		Action:    sessionsock.ActionRegister,
		Handle:    handle,
		SPIFFEID:  inst.SPIFFEID,
		SVID:      inst.KIT,
		Blueprint: blueprint,
		ULID:      inst.SessionID,
		AgentID:   blueprint,
		ExpiresAt: inst.ExpiresAt,
		SessionID: sid,
		Routes:    routes,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: register with daemon: %v\n", err)
		revokeErr := revokeSessionInstance(cfg, inst.SessionID)
		if revokeErr != nil {
			fmt.Fprintf(os.Stderr, "keydris session: rollback revoke failed; retained state %q for retry: %v\n", sid, revokeErr)
		} else {
			_ = removeState(cfg, sid)
		}
		unbindSessionForMode(cfg, handle)
		return 1
	}

	fmt.Fprintf(os.Stderr, "keydris: session %s bound to %s\n", sid, inst.SPIFFEID)
	return 0
}

func hookSessionEnd(cfg *config.Config, sid string) int {
	if err := validateSessionID(sid); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: %v\n", err)
		return 1
	}
	st, err := loadState(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: no state for session %q: %v\n", sid, err)
		return 1
	}

	currentULID := st.ULID
	currentSPIFFEID := st.SPIFFEID
	snapshot, unregisterErr := exchangeAuthenticatedSessionMessage(cfg, sessionsock.Message{
		Action: sessionsock.ActionUnregister, Handle: st.Handle,
	})
	if snapshot != nil {
		if snapshot.SessionID != "" && snapshot.SessionID != sid {
			unregisterErr = fmt.Errorf("daemon returned a different session id")
		} else {
			currentULID = snapshot.ULID
			currentSPIFFEID = snapshot.SPIFFEID
			// Retain the current renewed identity if revocation fails and this
			// SessionEnd must be retried after the daemon has unregistered it.
			st.ULID = snapshot.ULID
			st.SPIFFEID = snapshot.SPIFFEID
			st.KIT = snapshot.SVID
			st.ExpiresAt = snapshot.ExpiresAt
			_ = saveState(cfg, st)
		}
	}

	if err := revokeSessionInstance(cfg, currentULID); err != nil {
		fmt.Fprintf(os.Stderr, "keydris session: revoke: %v\n", err)
		unbindSessionForMode(cfg, st.Handle)
		return 1
	}
	if unregisterErr != nil {
		fmt.Fprintf(os.Stderr, "keydris session: unregister: %v\n", unregisterErr)
		return 1
	}
	unbindSessionForMode(cfg, st.Handle)
	_ = removeState(cfg, sid)

	fmt.Fprintf(os.Stderr, "keydris: session %s ended (%s revoked)\n", sid, currentSPIFFEID)
	return 0
}

// --- control-plane client ---

type mintedInstance = runtimecontract.KitSession

var (
	mintSessionInstance    = mintInstance
	revokeSessionInstance  = revokeInstance
	sendSessionMessage     = sessionsock.Send
	exchangeSessionMessage = sessionsock.Exchange
	fetchSessionRoutes     = fetchRoutesInstance
)

func fetchRoutesInstance(
	cfg *config.Config,
	runtimeToken string,
) (*runtimecontract.RuntimeRoutes, error) {
	client, err := mTLSClient(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return runtimecontract.FetchRuntimeRoutes(
		ctx,
		client,
		cfg.ControlMTLSURL,
		runtimeToken,
	)
}

func sendAuthenticatedSessionMessage(cfg *config.Config, message sessionsock.Message) error {
	_, err := exchangeAuthenticatedSessionMessageWith(cfg, message, func(path string, message sessionsock.Message) (*sessionsock.SessionSnapshot, error) {
		if err := sendSessionMessage(path, message); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return err
}

func exchangeAuthenticatedSessionMessage(
	cfg *config.Config,
	message sessionsock.Message,
) (*sessionsock.SessionSnapshot, error) {
	return exchangeAuthenticatedSessionMessageWith(cfg, message, exchangeSessionMessage)
}

func exchangeAuthenticatedSessionMessageWith(
	cfg *config.Config,
	message sessionsock.Message,
	exchange func(string, sessionsock.Message) (*sessionsock.SessionSnapshot, error),
) (*sessionsock.SessionSnapshot, error) {
	authPath := cfg.SessionAuthFile
	if authPath == "" {
		authPath = filepath.Join(cfg.DataDir, "session.auth")
	}
	secret, err := sessionsock.LoadOrCreateSecret(authPath)
	if err != nil {
		return nil, fmt.Errorf("session socket auth: %w", err)
	}
	message.Auth = secret
	return exchange(cfg.SessionSocket, message)
}

func lookupRegisteredSession(
	cfg *config.Config,
	handle string,
) (*sessionsock.SessionSnapshot, error) {
	snapshot, err := exchangeAuthenticatedSessionMessage(cfg, sessionsock.Message{
		Action: sessionsock.ActionLookup,
		Handle: handle,
	})
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.SVID == "" {
		return nil, fmt.Errorf("daemon returned no session")
	}
	return snapshot, nil
}

// mTLSClient builds the control-plane client that presents the login identity,
// returning an actionable error when the node has not run `keydris login`.
func mTLSClient(cfg *config.Config) (*http.Client, error) {
	if _, err := login.EnsureFresh(cfg.IdentityDir, cfg.ControlMTLSURL, cfg.MTLSServerCA, 48*time.Hour); err != nil {
		return nil, fmt.Errorf("renew control-plane mTLS identity: %w", err)
	}
	client, err := login.HTTPClient(cfg.IdentityDir, cfg.MTLSServerCA, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("control-plane mTLS identity (run `keydris login`): %w", err)
	}
	return client, nil
}

func mintInstance(cfg *config.Config, agentID, handle string) (*mintedInstance, error) {
	client, err := mTLSClient(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return runtimecontract.CreateKitSession(ctx, client, cfg.ControlMTLSURL, runtimecontract.CreateKitSessionInput{
		AgentID:        agentID,
		SessionHandle:  handle,
		IdempotencyKey: "cli-" + newProxyToken(),
	})
}

func revokeInstance(cfg *config.Config, ulid string) error {
	client, err := mTLSClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return runtimecontract.RevokeKitSession(ctx, client, cfg.ControlMTLSURL, ulid)
}

// --- session state (so SessionEnd can find the minted instance) ---

func stateDir(cfg *config.Config) string { return sessionstate.Dir(cfg.DataDir) }

func statePath(cfg *config.Config, sid string) string {
	path, _ := sessionstate.Path(cfg.DataDir, sid)
	return path
}

func validateSessionID(sid string) error {
	return sessionstate.ValidateID(sid)
}

func saveState(cfg *config.Config, st sessionState) error {
	return sessionstate.Save(cfg.DataDir, st)
}

func loadState(cfg *config.Config, sid string) (sessionState, error) {
	return sessionstate.Load(cfg.DataDir, sid)
}

func removeState(cfg *config.Config, sid string) error {
	return sessionstate.Remove(cfg.DataDir, sid)
}

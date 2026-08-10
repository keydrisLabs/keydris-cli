package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionstate"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const (
	sessionRenewPollInterval = 30 * time.Second
	sessionRenewWindow       = 5 * time.Minute
	sessionRenewTimeout      = 20 * time.Second
)

type sessionRenewalAPI interface {
	create(context.Context, runtimecontract.CreateKitSessionInput) (*runtimecontract.KitSession, error)
	routes(context.Context, string) (*runtimecontract.RuntimeRoutes, error)
	revoke(context.Context, string) error
}

type controlSessionRenewalAPI struct {
	client  *http.Client
	baseURL string
}

func (api controlSessionRenewalAPI) create(
	ctx context.Context,
	input runtimecontract.CreateKitSessionInput,
) (*runtimecontract.KitSession, error) {
	return runtimecontract.CreateKitSession(ctx, api.client, api.baseURL, input)
}

func (api controlSessionRenewalAPI) routes(
	ctx context.Context,
	token string,
) (*runtimecontract.RuntimeRoutes, error) {
	return runtimecontract.FetchRuntimeRoutes(ctx, api.client, api.baseURL, token)
}

func (api controlSessionRenewalAPI) revoke(ctx context.Context, sessionID string) error {
	return runtimecontract.RevokeKitSession(ctx, api.client, api.baseURL, sessionID)
}

func runSessionRenewalLoop(
	ctx context.Context,
	cfg *config.Config,
	client *http.Client,
	registry *attest.SessionRegistry,
	logf func(string, ...any),
) {
	ticker := time.NewTicker(sessionRenewPollInterval)
	defer ticker.Stop()
	api := controlSessionRenewalAPI{client: client, baseURL: cfg.ControlMTLSURL}
	deadOwners := make(map[string]time.Time)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			sessions := registry.Snapshot()
			registered := make(map[string]struct{}, len(sessions))
			for _, session := range sessions {
				registered[session.Handle] = struct{}{}
				if session.OwnerManaged {
					if session.OwnerPID <= 0 || session.OwnerIdentity == "" {
						logf(
							"managed session has no verifiable owner; renewal withheld: session=%s",
							session.SessionID,
						)
						continue
					}
					alive, aliveErr := ownerProcessMatches(session.OwnerPID, session.OwnerIdentity)
					if aliveErr != nil {
						logf(
							"session owner check failed: session=%s pid=%d: %v",
							session.SessionID,
							session.OwnerPID,
							aliveErr,
						)
						// Process identity is part of the authorization boundary. If
						// it cannot be checked, do not extend this KIT's lifetime.
						continue
					} else if !alive {
						firstSeen, seen := deadOwners[session.Handle]
						if !seen {
							deadOwners[session.Handle] = now
							// Give the wrapper one polling interval to perform its
							// synchronous SessionEnd cleanup and avoid racing it.
							continue
						}
						if now.Sub(firstSeen) < sessionRenewPollInterval {
							continue
						}
						retireCtx, cancel := context.WithTimeout(ctx, sessionRenewTimeout)
						err := retireExitedSession(retireCtx, cfg, registry, session, api)
						cancel()
						if err != nil {
							logf(
								"exited session cleanup failed: session=%s pid=%d: %v",
								session.SessionID,
								session.OwnerPID,
								err,
							)
						}
						delete(deadOwners, session.Handle)
						continue
					}
					delete(deadOwners, session.Handle)
				}
				if !sessionRenewalDue(session, now) {
					continue
				}
				renewCtx, cancel := context.WithTimeout(ctx, sessionRenewTimeout)
				err := renewRegisteredSession(renewCtx, cfg, registry, session, api)
				cancel()
				if err != nil {
					logf(
						"session renewal failed: session=%s spiffe=%s: %v",
						session.SessionID,
						session.SPIFFEID,
						err,
					)
				}
			}
			for handle := range deadOwners {
				if _, ok := registered[handle]; !ok {
					delete(deadOwners, handle)
				}
			}
		}
	}
}

// retireExitedSession stops renewal before revoking. Take returns the latest
// registry value, so an exit racing with a successful renewal revokes the
// replacement rather than the stale snapshot. On revoke failure the registry
// remains removed (fail closed) and durable state is retained for diagnosis or
// a later explicit SessionEnd retry; the unrenewed KIT will expire naturally.
func retireExitedSession(
	ctx context.Context,
	cfg *config.Config,
	registry *attest.SessionRegistry,
	snapshot attest.Session,
	api sessionRenewalAPI,
) error {
	current, ok := registry.Take(snapshot.Handle)
	if !ok {
		return nil
	}
	if err := api.revoke(ctx, current.ULID); err != nil {
		return fmt.Errorf("revoke current KIT: %w", err)
	}
	if current.SessionID != "" {
		if err := sessionstate.Remove(cfg.DataDir, current.SessionID); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove durable session state: %w", err)
		}
	}
	return nil
}

func sessionRenewalDue(session attest.Session, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, session.ExpiresAt)
	if err != nil {
		return true
	}
	window := sessionRenewWindow
	if !session.IssuedAt.IsZero() && expiresAt.After(session.IssuedAt) {
		if dynamic := expiresAt.Sub(session.IssuedAt) / 3; dynamic < window {
			window = dynamic
		}
	}
	if window < 10*time.Second {
		window = 10 * time.Second
	}
	return !expiresAt.After(now.Add(window))
}

func renewRegisteredSession(
	ctx context.Context,
	cfg *config.Config,
	registry *attest.SessionRegistry,
	current attest.Session,
	api sessionRenewalAPI,
) error {
	agentID := current.AgentID
	if agentID == "" {
		agentID = current.Blueprint
	}
	if agentID == "" || current.Handle == "" || current.ULID == "" {
		return fmt.Errorf("registered session is missing renewal identity")
	}
	renewed, err := api.create(ctx, runtimecontract.CreateKitSessionInput{
		AgentID:           agentID,
		SessionHandle:     current.Handle,
		IdempotencyKey:    renewalIdempotencyKey(current),
		ReplacesSessionID: current.ULID,
	})
	if err != nil {
		return err
	}
	routes, err := api.routes(ctx, renewed.KIT)
	if err != nil {
		_ = api.revoke(context.WithoutCancel(ctx), renewed.SessionID)
		return fmt.Errorf("fetch renewed runtime routes: %w", err)
	}
	if routes.Agent.AgentID != agentID {
		_ = api.revoke(context.WithoutCancel(ctx), renewed.SessionID)
		return fmt.Errorf("renewed runtime routes agent does not match the session")
	}

	next := current
	next.SPIFFEID = renewed.SPIFFEID
	next.SVID = renewed.KIT
	next.ULID = renewed.SessionID
	next.ExpiresAt = renewed.ExpiresAt
	next.IssuedAt = time.Now().UTC()
	next.Routes = routes
	replaced, persistErr := registry.ReplaceWith(
		current.Handle,
		current.ULID,
		next,
		func() error {
			if current.SessionID == "" {
				return nil
			}
			return sessionstate.Save(cfg.DataDir, sessionstate.State{
				SessionID:     current.SessionID,
				Handle:        current.Handle,
				ULID:          renewed.SessionID,
				SPIFFEID:      renewed.SPIFFEID,
				Blueprint:     current.Blueprint,
				ExpiresAt:     renewed.ExpiresAt,
				OwnerPID:      current.OwnerPID,
				OwnerManaged:  current.OwnerManaged,
				OwnerIdentity: current.OwnerIdentity,
				KIT:           renewed.KIT,
				Routes:        routes,
			})
		},
	)
	if !replaced {
		if err := api.revoke(context.WithoutCancel(ctx), renewed.SessionID); err != nil {
			return fmt.Errorf("session ended during renewal; revoke replacement: %w", err)
		}
		return nil
	}
	if persistErr != nil {
		// The daemon now holds the valid replacement and the authenticated local
		// lookup path exposes it to hooks. Keep it active and surface the durable
		// state error rather than revoking a session that is already in use.
		return fmt.Errorf("persist renewed session: %w", persistErr)
	}
	return nil
}

func renewalIdempotencyKey(session attest.Session) string {
	digest := sha256.Sum256([]byte(session.Handle))
	return "renew-" + session.ULID + "-" + hex.EncodeToString(digest[:8])
}

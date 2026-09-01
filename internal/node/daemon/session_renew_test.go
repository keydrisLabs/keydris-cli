package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionstate"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestOwnerProcessMatchesRejectsWrongCreationIdentity(t *testing.T) {
	matched, err := ownerProcessMatches(os.Getpid(), "not-this-process")
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("owner check accepted a mismatched process-creation identity")
	}
}

const (
	testRenewOldULID = "01K1X4Y5Z6A7B8C9D0E1F2G3H5"
	testRenewNewULID = "01K1X4Y5Z6A7B8C9D0E1F2G3H6"
	testRenewAgentID = "33333333-3333-4333-8333-333333333333"
)

type fakeSessionRenewalAPI struct {
	created     runtimecontract.CreateKitSessionInput
	renewed     *runtimecontract.KitSession
	routesValue *runtimecontract.RuntimeRoutes
	revoked     []string
	onRoutes    func()
}

func (api *fakeSessionRenewalAPI) create(
	_ context.Context,
	input runtimecontract.CreateKitSessionInput,
) (*runtimecontract.KitSession, error) {
	api.created = input
	return api.renewed, nil
}

func (api *fakeSessionRenewalAPI) routes(
	_ context.Context,
	_ string,
) (*runtimecontract.RuntimeRoutes, error) {
	if api.onRoutes != nil {
		api.onRoutes()
	}
	return api.routesValue, nil
}

func (api *fakeSessionRenewalAPI) revoke(_ context.Context, sessionID string) error {
	api.revoked = append(api.revoked, sessionID)
	return nil
}

func TestRenewRegisteredSessionReplacesRegistryAndState(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	registry := attest.NewSessionRegistry()
	current := testRenewSession(time.Now().Add(time.Minute))
	registry.Register(current)
	if err := sessionstate.Save(dir, stateFromAttested(current)); err != nil {
		t.Fatal(err)
	}
	api := testRenewalAPI()

	if err := renewRegisteredSession(context.Background(), cfg, registry, current, api); err != nil {
		t.Fatal(err)
	}

	got, ok := registry.Lookup(current.Handle)
	if !ok || got.ULID != testRenewNewULID || got.SVID != "renewed-kit" {
		t.Fatalf("registry was not renewed: %+v, ok=%v", got, ok)
	}
	state, err := sessionstate.Load(dir, current.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ULID != testRenewNewULID || state.KIT != "renewed-kit" {
		t.Fatalf("durable state was not renewed: %+v", state)
	}
	if api.created.ReplacesSessionID != testRenewOldULID ||
		api.created.SessionHandle != current.Handle {
		t.Fatalf("wrong replacement request: %+v", api.created)
	}
}

func TestRenewRegisteredSessionRevokesReplacementAfterConcurrentEnd(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	registry := attest.NewSessionRegistry()
	current := testRenewSession(time.Now().Add(time.Minute))
	registry.Register(current)
	api := testRenewalAPI()
	api.onRoutes = func() { registry.Unregister(current.Handle) }

	if err := renewRegisteredSession(context.Background(), cfg, registry, current, api); err != nil {
		t.Fatal(err)
	}
	if len(api.revoked) != 1 || api.revoked[0] != testRenewNewULID {
		t.Fatalf("orphan replacement was not revoked: %v", api.revoked)
	}
}

func TestRetireExitedSessionRevokesLatestAndRemovesState(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	registry := attest.NewSessionRegistry()
	snapshot := testRenewSession(time.Now().Add(time.Minute))
	current := snapshot
	current.ULID = testRenewNewULID
	current.SVID = "renewed-kit"
	current.OwnerManaged = true
	current.OwnerIdentity = "test-owner"
	registry.Register(current)
	if err := sessionstate.Save(dir, stateFromAttested(current)); err != nil {
		t.Fatal(err)
	}
	api := testRenewalAPI()

	if err := retireExitedSession(context.Background(), cfg, registry, snapshot, api); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup(current.Handle); ok {
		t.Fatal("exited session remained registered")
	}
	if len(api.revoked) != 1 || api.revoked[0] != testRenewNewULID {
		t.Fatalf("latest session was not revoked: %v", api.revoked)
	}
	if _, err := sessionstate.Load(dir, current.SessionID); err == nil {
		t.Fatal("exited session state was not removed")
	}
}

func TestSessionRenewalDue(t *testing.T) {
	now := time.Now().UTC()
	if sessionRenewalDue(testRenewSession(now.Add(10*time.Minute)), now) {
		t.Fatal("session was renewed too early")
	}
	if !sessionRenewalDue(testRenewSession(now.Add(4*time.Minute)), now) {
		t.Fatal("session inside renewal window was not due")
	}

	short := testRenewSession(now.Add(90 * time.Second))
	short.IssuedAt = now
	if sessionRenewalDue(short, now.Add(30*time.Second)) {
		t.Fatal("short-lived session was renewed before its one-third window")
	}
	if !sessionRenewalDue(short, now.Add(61*time.Second)) {
		t.Fatal("short-lived session inside its one-third window was not due")
	}
}

func testRenewSession(expiresAt time.Time) attest.Session {
	routes := testRenewRoutes()
	return attest.Session{
		Handle:    "session-handle",
		SPIFFEID:  "spiffe://keydris.test/old",
		SVID:      "old-kit",
		Blueprint: testRenewAgentID,
		AgentID:   testRenewAgentID,
		ULID:      testRenewOldULID,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		SessionID: "claude-session",
		OwnerPID:  42,
		Routes:    routes,
	}
}

func testRenewalAPI() *fakeSessionRenewalAPI {
	return &fakeSessionRenewalAPI{
		renewed: &runtimecontract.KitSession{
			SessionID: testRenewNewULID,
			SPIFFEID:  "spiffe://keydris.test/new",
			KIT:       "renewed-kit",
			ExpiresAt: time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
		},
		routesValue: testRenewRoutes(),
	}
}

func testRenewRoutes() *runtimecontract.RuntimeRoutes {
	return &runtimecontract.RuntimeRoutes{
		SchemaVersion:  1,
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		Agent: runtimecontract.RoutesAgent{
			AgentID:     testRenewAgentID,
			DisplayName: "Renewed agent",
		},
		Routes: []runtimecontract.RuntimeRoute{},
	}
}

func stateFromAttested(session attest.Session) sessionstate.State {
	return sessionstate.State{
		SessionID:     session.SessionID,
		Handle:        session.Handle,
		ULID:          session.ULID,
		SPIFFEID:      session.SPIFFEID,
		Blueprint:     session.Blueprint,
		ExpiresAt:     session.ExpiresAt,
		OwnerPID:      session.OwnerPID,
		OwnerManaged:  session.OwnerManaged,
		OwnerIdentity: session.OwnerIdentity,
		KIT:           session.SVID,
		Routes:        session.Routes,
	}
}

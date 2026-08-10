package sessionsock

import (
	"path/filepath"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestServerRequiresAuthentication(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "session.auth")
	secret, err := LoadOrCreateSecret(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := LoadOrCreateSecret(authPath); err != nil || again != secret {
		t.Fatalf("secret was not stable: got %q, err=%v", again, err)
	}

	registry := attest.NewSessionRegistry()
	socketPath := filepath.Join(dir, "registry.sock")
	server, err := Serve(socketPath, secret, registry, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	message := Message{
		Auth:      "wrong",
		Action:    ActionRegister,
		Handle:    "session-token",
		SPIFFEID:  "spiffe://keydris.test/agent/policy/id",
		SVID:      "signed-svid",
		ULID:      "01K1X4Y5Z6A7B8C9D0E1F2G3H5",
		SessionID: "claude-session",
		Routes:    testRuntimeRoutes(),
	}
	if err := Send(socketPath, message); err == nil {
		t.Fatal("unauthenticated registration was accepted")
	}
	if _, ok := registry.Lookup(message.Handle); ok {
		t.Fatal("unauthenticated registration changed the registry")
	}

	message.Auth = secret
	if err := Send(socketPath, message); err != nil {
		t.Fatalf("authenticated registration: %v", err)
	}
	if session, ok := registry.Lookup(message.Handle); !ok || session.SPIFFEID != message.SPIFFEID {
		t.Fatalf("registration missing or wrong: %+v, ok=%v", session, ok)
	}
	if err := Send(socketPath, Message{
		Auth: secret, Action: ActionUpdateOwner, Handle: message.Handle,
		OwnerPID: 42, OwnerManaged: true, OwnerIdentity: "test-owner",
	}); err != nil {
		t.Fatalf("update owner: %v", err)
	}
	if session, ok := registry.Lookup(message.Handle); !ok ||
		session.OwnerPID != 42 || !session.OwnerManaged || session.OwnerIdentity != "test-owner" {
		t.Fatalf("managed owner was not recorded: %+v, ok=%v", session, ok)
	}

	snapshot, err := Exchange(socketPath, Message{
		Auth: secret, Action: ActionLookup, Handle: message.Handle,
	})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if snapshot == nil || snapshot.SVID != message.SVID ||
		snapshot.SessionID != message.SessionID || !snapshot.OwnerManaged ||
		snapshot.OwnerIdentity != "test-owner" {
		t.Fatalf("lookup returned the wrong session: %+v", snapshot)
	}

	snapshot, err = Exchange(socketPath, Message{
		Auth: secret, Action: ActionUnregister, Handle: message.Handle,
	})
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if snapshot == nil || snapshot.ULID != message.ULID {
		t.Fatalf("unregister did not return the current identity: %+v", snapshot)
	}
	if _, ok := registry.Lookup(message.Handle); ok {
		t.Fatal("unregister did not remove the session")
	}
}

func testRuntimeRoutes() *runtimecontract.RuntimeRoutes {
	return &runtimecontract.RuntimeRoutes{
		SchemaVersion:  1,
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		Agent: runtimecontract.RoutesAgent{
			AgentID:     "33333333-3333-4333-8333-333333333333",
			DisplayName: "Test agent",
		},
		Routes: []runtimecontract.RuntimeRoute{},
	}
}

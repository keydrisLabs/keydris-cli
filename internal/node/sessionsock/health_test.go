package sessionsock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
)

func TestHealthAuthenticatedAndContainsNoSessionCredentials(t *testing.T) {
	dir, err := os.MkdirTemp("", "kh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "r.sock")
	server, err := Serve(path, "test-secret", attest.NewSessionRegistry(), func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := Inspect(path, "wrong-secret"); err == nil {
		t.Fatal("unauthenticated health accepted")
	}
	health, err := Inspect(path, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if health.Ready {
		t.Fatal("uninitialized data plane reported ready")
	}
	server.MarkReady(15001, "sandbox", "agent")
	health, err = Inspect(path, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !health.Ready || health.PID != os.Getpid() || health.Sessions != 0 {
		t.Fatalf("unexpected health: %+v", health)
	}
	body, _ := json.Marshal(health)
	for _, sensitive := range []string{"secret", "svid", "kit", "handle"} {
		if strings.Contains(string(body), sensitive) {
			t.Fatalf("health contains %s", sensitive)
		}
	}
}

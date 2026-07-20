package cli

import (
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

func TestRunProxyScopeAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_MANAGED_MODE", "")
	t.Setenv("KEYDRIS_MANAGED_DESTINATIONS", "")

	if code := runProxyScope([]string{"add", "https://Example.COM"}); code != 0 {
		t.Fatalf("add code = %d", code)
	}
	state, err := config.ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeSelected || len(state.Destinations) != 1 || state.Destinations[0] != "example.com:443" {
		t.Fatalf("state after add = %+v", state)
	}

	if code := runProxyScope([]string{"remove", "example.com:443"}); code != 0 {
		t.Fatalf("remove code = %d", code)
	}
	state, err = config.ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeSelected || len(state.Destinations) != 0 {
		t.Fatalf("state after remove = %+v", state)
	}
}

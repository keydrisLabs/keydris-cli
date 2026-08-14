package cli

import (
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
)

// Scope comes from policy, so reject manual edits that would be overwritten on restart.
func TestRunProxyScopeMutationsRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_MANAGED_MODE", "")
	t.Setenv("KEYDRIS_MANAGED_DESTINATIONS", "")

	if err := config.SaveDerivedManagedScope(dir, []string{"api.test:8443"}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"add", "https://example.com"},
		{"remove", "api.test:8443"},
		{"all"},
		{"bogus"},
		{},
	} {
		if code := runProxyScope(args); code == 0 {
			t.Errorf("runProxyScope(%q) = 0, want non-zero", args)
		}
	}

	// A retired subcommand must get the pointed message, not bare usage —
	// with or without the operand it used to take.
	for _, args := range [][]string{
		{"add", "https://example.com"},
		{"add"},
		{"remove", "api.test:8443"},
		{"all"},
	} {
		action, subcommand := classifyProxyScopeArgs(args)
		if action != proxyScopeRetired {
			t.Errorf("classifyProxyScopeArgs(%q) = %v, want retired", args, action)
		}
		if subcommand != args[0] {
			t.Errorf("classifyProxyScopeArgs(%q) subcommand = %q", args, subcommand)
		}
	}

	// Anything else is a plain usage error.
	for _, args := range [][]string{{}, {"bogus"}, {"list", "extra"}} {
		if action, _ := classifyProxyScopeArgs(args); action != proxyScopeUsage {
			t.Errorf("classifyProxyScopeArgs(%q) = %v, want usage", args, action)
		}
	}
	if action, _ := classifyProxyScopeArgs([]string{"list"}); action != proxyScopeList {
		t.Errorf("classifyProxyScopeArgs([list]) = %v, want list", action)
	}

	// None of the rejected calls may have touched the derived scope.
	state, err := config.ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Source != config.ManagedScopeSourcePolicy ||
		len(state.Destinations) != 1 || state.Destinations[0] != "api.test:8443" {
		t.Fatalf("scope was modified: %+v", state)
	}
}

func TestRunProxyScopeList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KEYDRIS_DATA_DIR", dir)
	t.Setenv("KEYDRIS_MANAGED_MODE", "")
	t.Setenv("KEYDRIS_MANAGED_DESTINATIONS", "")

	if err := config.SaveDerivedManagedScope(dir, []string{"api.test:8443"}); err != nil {
		t.Fatal(err)
	}
	if code := runProxyScope([]string{"list"}); code != 0 {
		t.Fatalf("list code = %d", code)
	}
	// A trailing argument is a usage error, not an ignored extra.
	if code := runProxyScope([]string{"list", "extra"}); code == 0 {
		t.Fatal("list with an extra argument = 0, want non-zero")
	}
}

package config

import (
	"os"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

func TestManagedScopePersistence(t *testing.T) {
	dir := t.TempDir()
	if err := SaveManagedScope(dir, proxyscope.ModeSelected, []string{"Example.COM", "api.test:8443"}); err != nil {
		t.Fatal(err)
	}
	state, err := ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeSelected {
		t.Fatalf("Mode = %q", state.Mode)
	}
	if len(state.Destinations) != 2 || state.Destinations[0] != "api.test:8443" || state.Destinations[1] != "example.com:443" {
		t.Fatalf("Destinations = %#v", state.Destinations)
	}
	info, err := os.Stat(managedScopePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestManagedScopeMissingDefaultsAll(t *testing.T) {
	state, err := ReadManagedScope(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeAll {
		t.Fatalf("Mode = %q", state.Mode)
	}
}

func TestManagedScopeCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(managedScopePath(dir), []byte(`{"mode":`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := loadManagedScope(dir)
	if err == nil {
		t.Fatal("expected corrupt scope error")
	}
	if state.Mode != proxyscope.ModeSelected || len(state.Destinations) != 0 {
		t.Fatalf("corrupt scope fallback = %+v", state)
	}
}

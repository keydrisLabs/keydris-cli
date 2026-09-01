package config

import (
	"os"
	"runtime"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

func TestManagedScopePersistence(t *testing.T) {
	dir := t.TempDir()
	if err := SaveDerivedManagedScope(dir, []string{"Example.COM", "api.test:8443"}); err != nil {
		t.Fatal(err)
	}
	state, err := ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeSelected {
		t.Fatalf("Mode = %q", state.Mode)
	}
	if state.Source != ManagedScopeSourcePolicy {
		t.Fatalf("Source = %q, want %q", state.Source, ManagedScopeSourcePolicy)
	}
	if len(state.Destinations) != 2 || state.Destinations[0] != "api.test:8443" || state.Destinations[1] != "example.com:443" {
		t.Fatalf("Destinations = %#v", state.Destinations)
	}
	info, err := os.Stat(managedScopePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

// An empty policy manages nothing; a missing file manages everything.
func TestManagedScopeEmptyPolicyManagesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := SaveDerivedManagedScope(dir, nil); err != nil {
		t.Fatal(err)
	}
	state, err := ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeSelected || len(state.Destinations) != 0 {
		t.Fatalf("state = %+v, want selected with no destinations", state)
	}
}

// Files predating auto-detection carry no "source" and must still load.
func TestManagedScopeWithoutSourceLoads(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"mode":"selected","destinations":["api.test:8443"]}`)
	if err := os.WriteFile(managedScopePath(dir), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Source != "" {
		t.Fatalf("Source = %q, want empty for a legacy file", state.Source)
	}
	if len(state.Destinations) != 1 || state.Destinations[0] != "api.test:8443" {
		t.Fatalf("Destinations = %#v", state.Destinations)
	}
}

func TestRemoveManagedScopeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := SaveDerivedManagedScope(dir, []string{"api.test:8443"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RemoveManagedScope(dir); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
	}
	// Back to the historical default once the cache is gone.
	state, err := ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != proxyscope.ModeAll {
		t.Fatalf("Mode = %q, want %q", state.Mode, proxyscope.ModeAll)
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

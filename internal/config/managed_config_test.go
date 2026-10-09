//go:build darwin || linux

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManagedFixture(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagedConfigIsAuthoritative(t *testing.T) {
	managed := writeManagedFixture(t, "[keydris]\ncontrol_url = 'https://managed.example'\n", 0o644)
	user := filepath.Join(t.TempDir(), ".keydris.toml")
	if err := os.WriteFile(user, []byte("control_url = 'https://user.example'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEYDRIS_CONTROL_URL", "https://environment.example")
	sources := map[string]string{"KEYDRIS_CONTROL_URL": "environment"}
	if err := validateManagedConfig(managed, uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTomlLayer(managed, "managed: "+managed, true, true, sources); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTomlLayer(user, "user", false, false, sources); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KEYDRIS_CONTROL_URL"); got != "https://managed.example" {
		t.Fatalf("control URL = %q, want managed value", got)
	}
	if got := sources["KEYDRIS_CONTROL_URL"]; got != "managed: "+managed {
		t.Fatalf("source = %q", got)
	}
}

func TestManagedConfigRejectsUnsafePermissions(t *testing.T) {
	path := writeManagedFixture(t, "control_url = 'https://managed.example'\n", 0o666)
	err := validateManagedConfig(path, uint32(os.Getuid()))
	if err == nil || !strings.Contains(err.Error(), "writable by group or other users") {
		t.Fatalf("unsafe managed config error = %v", err)
	}
}

func TestManagedConfigRejectsWrongOwner(t *testing.T) {
	path := writeManagedFixture(t, "control_url = 'https://managed.example'\n", 0o644)
	err := validateManagedConfig(path, uint32(os.Getuid()+1))
	if err == nil || !strings.Contains(err.Error(), "must be owned") {
		t.Fatalf("wrong owner error = %v", err)
	}
}

func TestManagedConfigRejectsSymlink(t *testing.T) {
	target := writeManagedFixture(t, "control_url = 'https://managed.example'\n", 0o644)
	link := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := validateManagedConfig(link, uint32(os.Getuid()))
	if err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestManagedConfigRejectsMalformedContent(t *testing.T) {
	path := writeManagedFixture(t, "control_url = 'https://must-not-apply.example'\nbroken line\n", 0o644)
	t.Setenv("KEYDRIS_CONTROL_URL", "https://original.example")
	if _, err := loadTomlLayer(path, "managed", true, true, map[string]string{}); err == nil {
		t.Fatal("malformed managed config was accepted")
	}
	if got := os.Getenv("KEYDRIS_CONTROL_URL"); got != "https://original.example" {
		t.Fatalf("partially applied rejected config: %q", got)
	}
}

func TestManagedEmptyValueStillLocksSetting(t *testing.T) {
	managed := writeManagedFixture(t, "oauth_client_secret = ''\n", 0o644)
	user := filepath.Join(t.TempDir(), ".keydris.toml")
	if err := os.WriteFile(user, []byte("oauth_client_secret = 'user-secret'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEYDRIS_OAUTH_CLIENT_SECRET", "environment-secret")
	sources := map[string]string{"KEYDRIS_OAUTH_CLIENT_SECRET": "environment"}
	if _, err := loadTomlLayer(managed, "managed: "+managed, true, true, sources); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTomlLayer(user, "user", false, false, sources); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KEYDRIS_OAUTH_CLIENT_SECRET"); got != "" {
		t.Fatalf("managed empty value was overridden: %q", got)
	}
}

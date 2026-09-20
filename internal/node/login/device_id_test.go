package login

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceIDSurvivesLogout(t *testing.T) {
	dir := t.TempDir()
	id, err := enrollmentDeviceID(dir)
	if err != nil || !deviceUUID.MatchString(id) {
		t.Fatalf("invalid installation ID: %q, %v", id, err)
	}
	for _, name := range []string{KeyFile, CertFile, CAFile} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("credential"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Logout(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{KeyFile, CertFile, CAFile, WhoamiFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("logout retained %s", name)
		}
	}
	again, err := enrollmentDeviceID(dir)
	if err != nil || again != id {
		t.Fatalf("logout changed device identity: %q, %v", again, err)
	}
	other, err := enrollmentDeviceID(t.TempDir())
	if err != nil || other == id {
		t.Fatal("separate installations must have separate IDs")
	}
}

func TestDeviceIDMigratesExistingIdentity(t *testing.T) {
	dir := t.TempDir()
	const id = "11111111-1111-4111-8111-111111111111"
	if err := os.WriteFile(filepath.Join(dir, WhoamiFile), []byte(`{"device_id":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Logout(dir); err != nil {
		t.Fatal(err)
	}
	got, err := enrollmentDeviceID(dir)
	if err != nil || got != id {
		t.Fatalf("lost existing device ID: %q, %v", got, err)
	}
}

func TestDeviceIDDoesNotReplaceCorruptMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, WhoamiFile), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollmentDeviceID(dir); err == nil {
		t.Fatal("corrupt metadata must not create a duplicate device")
	}
}

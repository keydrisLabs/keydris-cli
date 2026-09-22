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

// TestDeviceIDRejectsInvalidOrUnreadableState pins the fail-closed enrollment
// gate: malformed state stops enrollment instead of allocating a second
// installation identity the server would treat as a new device.
func TestDeviceIDRejectsInvalidOrUnreadableState(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(DeviceIDFile, "not-a-uuid\n")
	if _, err := enrollmentDeviceID(dir); err == nil {
		t.Fatal("invalid stored device ID was replaced")
	}
	if err := os.Remove(filepath.Join(dir, DeviceIDFile)); err != nil {
		t.Fatal(err)
	}

	write(WhoamiFile, `{"device_id":"not-a-uuid"}`)
	if _, err := enrollmentDeviceID(dir); err == nil {
		t.Fatal("invalid identity metadata device ID was replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, DeviceIDFile)); !os.IsNotExist(err) {
		t.Fatal("rejected metadata still created a device ID file")
	}
	if err := os.Remove(filepath.Join(dir, WhoamiFile)); err != nil {
		t.Fatal(err)
	}

	// An unreadable entry point (a directory) is an error, not absence.
	if err := os.Mkdir(filepath.Join(dir, DeviceIDFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollmentDeviceID(dir); err == nil {
		t.Fatal("unreadable device ID path was treated as absent")
	}
	if err := os.Remove(filepath.Join(dir, DeviceIDFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, WhoamiFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollmentDeviceID(dir); err == nil {
		t.Fatal("unreadable identity metadata was treated as absent")
	}
}

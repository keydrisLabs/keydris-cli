package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCABundleKeepsSystemAndKeydrisRoots(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.pem")
	keydrisPath := filepath.Join(dir, "keydris.pem")
	bundlePath := filepath.Join(dir, "combined.pem")
	system := "-----BEGIN CERTIFICATE-----\nsystem-root\n-----END CERTIFICATE-----\n"
	keydris := "-----BEGIN CERTIFICATE-----\nkeydris-root\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(systemPath, []byte(system), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keydrisPath, []byte(keydris), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", systemPath)

	if err := BuildCABundle(keydrisPath, bundlePath); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "system-root") || !strings.Contains(string(body), "keydris-root") {
		t.Fatalf("combined bundle = %s", body)
	}
}

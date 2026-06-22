//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// installTrustStore copies the CA into the system anchor directory and refreshes
// the trust store. Needs root (or CAP) to write under /usr/local/share; callers
// should run it via `sudo keydris init --trust-store`. The env-var layer covers
// the common tools without root.
func installTrustStore(caPath string) error {
	src, err := os.ReadFile(caPath)
	if err != nil {
		return err
	}
	dst := "/usr/local/share/ca-certificates/keydris-ca.crt"
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		return fmt.Errorf("write %s (need root?): %w", dst, err)
	}
	if _, err := exec.LookPath("update-ca-certificates"); err == nil {
		if out, err := exec.Command("update-ca-certificates").CombinedOutput(); err != nil {
			return fmt.Errorf("update-ca-certificates: %w: %s", err, out)
		}
		return nil
	}
	// Fedora/RHEL family.
	if _, err := exec.LookPath("update-ca-trust"); err == nil {
		if out, err := exec.Command("update-ca-trust", "extract").CombinedOutput(); err != nil {
			return fmt.Errorf("update-ca-trust: %w: %s", err, out)
		}
	}
	return nil
}

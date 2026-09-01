//go:build windows

package sandbox

import (
	"fmt"
	"os/exec"
)

// installTrustStore adds the CA to the current user's Windows root store.
// The user scope avoids requiring an elevated terminal.
func installTrustStore(caPath string) error {
	cmd := exec.Command("certutil.exe", "-user", "-addstore", "Root", caPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("certutil addstore: %w: %s", err, out)
	}
	return nil
}

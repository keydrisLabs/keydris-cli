//go:build windows

package sandbox

import (
	"crypto/sha1"
	"crypto/x509"
	"fmt"
	"os/exec"
	"syscall"
)

func removeTrustStore(cert *x509.Certificate) error {
	// Windows certificate stores use SHA-1 thumbprints as identifiers. This is
	// an exact certificate lookup, not a trust/signature verification algorithm.
	fingerprint := fmt.Sprintf("%X", sha1.Sum(cert.Raw))
	script := fmt.Sprintf("$ErrorActionPreference='Stop'; $certPath='Cert:\\CurrentUser\\Root\\%s'; if (Test-Path -LiteralPath $certPath) { Remove-Item -LiteralPath $certPath -ErrorAction Stop }", fingerprint)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove CA from current-user Root store: %w: %s", err, out)
	}
	return nil
}

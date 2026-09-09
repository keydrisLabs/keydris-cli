//go:build linux

package sandbox

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
)

func removeTrustStore(cert *x509.Certificate) error {
	const installed = "/usr/local/share/ca-certificates/keydris-ca.crt"
	info, err := os.Lstat(installed)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular trust file %s", installed)
	}
	original, err := os.ReadFile(installed)
	if err != nil {
		return err
	}
	current, err := trustCertificate(original)
	if err != nil {
		return err
	}
	if !bytes.Equal(current.Raw, cert.Raw) {
		return nil
	}
	command, args := "update-ca-certificates", []string{}
	if _, err := exec.LookPath(command); err != nil {
		command, args = "update-ca-trust", []string{"extract"}
		if _, err := exec.LookPath(command); err != nil {
			return fmt.Errorf("no system trust refresh tool; remove CA trust manually or use --keep-trust")
		}
	}
	if err := os.Remove(installed); err != nil {
		return fmt.Errorf("remove %s (requires permission): %w", installed, err)
	}
	if out, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		restoreErr := os.WriteFile(installed, original, info.Mode().Perm())
		return fmt.Errorf("refresh trust failed: %w: %s (restoring original file: %v)", err, out, restoreErr)
	}
	return nil
}

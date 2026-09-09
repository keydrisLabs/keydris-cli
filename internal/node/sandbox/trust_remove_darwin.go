//go:build darwin

package sandbox

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os/exec"
)

func removeTrustStore(cert *x509.Certificate) error {
	keychain := expandLoginKeychain()
	out, err := exec.Command("security", "find-certificate", "-a", "-p", keychain).CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect login keychain: %w: %s", err, out)
	}
	found := false
	for len(out) > 0 {
		block, rest := pem.Decode(out)
		if block == nil {
			break
		}
		out = rest
		if bytes.Equal(block.Bytes, cert.Raw) {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	fingerprint := fmt.Sprintf("%X", sha256.Sum256(cert.Raw))
	if out, err := exec.Command("security", "delete-certificate", "-t", "-Z", fingerprint, keychain).CombinedOutput(); err != nil {
		return fmt.Errorf("remove CA from login keychain: %w: %s", err, out)
	}
	return nil
}

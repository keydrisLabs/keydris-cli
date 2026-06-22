//go:build darwin

package sandbox

import (
	"fmt"
	"os/exec"
)

// installTrustStore adds the CA to the macOS login keychain as a trusted root.
// This needs the user to authorize the keychain modification. For most sandbox
// tools the env-var layer (caEnvKeys) is sufficient and avoids this prompt.
func installTrustStore(caPath string) error {
	cmd := exec.Command("security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", expandLoginKeychain(),
		caPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("security add-trusted-cert: %w: %s", err, out)
	}
	return nil
}

func expandLoginKeychain() string {
	if out, err := exec.Command("sh", "-c", "echo $HOME/Library/Keychains/login.keychain-db").Output(); err == nil {
		if s := trimLine(out); s != "" {
			return s
		}
	}
	return "login.keychain"
}

func trimLine(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}

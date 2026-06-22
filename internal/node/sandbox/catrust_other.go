//go:build !darwin && !linux

package sandbox

import "fmt"

// installTrustStore is unsupported off macOS/Linux; the env-var layer still
// applies. Native Windows has no Claude Code sandbox anyway (use WSL2).
func installTrustStore(caPath string) error {
	return fmt.Errorf("OS trust-store install is not supported on this platform; the CA env vars are set instead (CA at %s)", caPath)
}

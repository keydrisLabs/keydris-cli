//go:build !darwin && !linux && !windows

package sandbox

import "fmt"

// installTrustStore is unsupported on this platform; the env-var layer still
// applies.
func installTrustStore(caPath string) error {
	return fmt.Errorf("OS trust-store install is not supported on this platform; the CA env vars are set instead (CA at %s)", caPath)
}

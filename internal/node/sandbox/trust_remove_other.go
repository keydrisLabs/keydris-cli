//go:build !windows && !linux && !darwin

package sandbox

import (
	"crypto/x509"
	"fmt"
)

func removeTrustStore(_ *x509.Certificate) error {
	return fmt.Errorf("trust cleanup is not supported on this OS; remove trust manually or use --keep-trust")
}

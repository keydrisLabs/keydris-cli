package sandbox

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// RemoveTrustStore removes only the certificate represented by caPath, using
// its fingerprint. It never removes certificates by display name.
func RemoveTrustStore(caPath string) error {
	data, err := os.ReadFile(caPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cert, err := trustCertificate(data)
	if err != nil {
		return err
	}
	return removeTrustStore(cert)
}
func trustCertificate(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid CA certificate; keep the original CA file until trust cleanup is complete")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("refusing trust cleanup for a non-CA certificate")
	}
	return cert, nil
}

package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// CA is a minimal certificate authority used by the sandbox proxy's HTTPS
// interception path: to inject a credential into an HTTPS upstream the proxy
// must terminate the agent's TLS with a leaf cert minted by a CA the agent
// trusts. Inside Claude Code's sandbox that trust is established by installing
// this CA (see internal/node/sandbox), so there is no certificate-pinning problem.
//
// GenerateCA + LeafFor are the primitives; ca.go persists the CA and exposes
// ServerTLSConfig, which the CONNECT handler in internal/node/dataplane/sandboxproxy.go
// uses to serve a per-SNI leaf on demand.
type CA struct {
	cert *x509.Certificate
	der  []byte
	key  *ecdsa.PrivateKey
}

// GenerateCA mints a fresh ECDSA root CA valid for the given duration.
func GenerateCA(commonName string, ttl time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Keydris POC"}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(ttl),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, der: der, key: key}, nil
}

// LeafFor mints a short-lived leaf certificate for host, signed by the CA, for
// use as a tls.Certificate when terminating an intercepted TLS connection.
func (c *CA) LeafFor(host string) (tls.Certificate, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &leafKey.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: [][]byte{der, c.der},
		PrivateKey:  leafKey,
		Leaf:        tmpl,
	}, nil
}

// CertPEM returns the CA certificate in PEM form for installation in a trust
// store.
func (c *CA) CertPEM() []byte {
	return pemBlock("CERTIFICATE", c.der)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	return n
}

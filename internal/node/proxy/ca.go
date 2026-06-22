package proxy

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// This file makes the in-memory CA (mitm.go) durable and wires it into a
// tls.Config so the sandbox proxy can terminate TLS with leaves the Claude Code
// sandbox already trusts (the CA is installed in the sandbox via internal/node/sandbox).
//
// The CA must be stable across daemon restarts: once its certificate is in the
// sandbox/OS trust store, regenerating it on every boot would break every TLS
// handshake until the operator re-installs the new root. So `keydris init`
// persists the CA and the daemon loads it.

// KeyPEM returns the CA private key in SEC1 EC PEM form for persistence.
func (c *CA) KeyPEM() ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(c.key)
	if err != nil {
		return nil, err
	}
	return pemBlock("EC PRIVATE KEY", der), nil
}

// Save writes the CA certificate and private key to disk. The key file is
// created with 0600 perms; the certificate with 0644 so it can be installed.
func (c *CA) Save(certPath, keyPath string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return err
	}
	keyPEM, err := c.KeyPEM()
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, c.CertPEM(), 0o644); err != nil {
		return fmt.Errorf("write CA cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write CA key: %w", err)
	}
	return nil
}

// LoadCA reconstructs a CA from a PEM certificate and key on disk.
func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("CA cert file is not a PEM CERTIFICATE")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("CA key file is not PEM")
	}
	key, err := parseECKey(keyBlock)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}
	return &CA{cert: cert, der: certBlock.Bytes, key: key}, nil
}

func parseECKey(b *pem.Block) (*ecdsa.PrivateKey, error) {
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	// Fall back to PKCS#8 in case the key was written in that form.
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("CA key is not an ECDSA key")
	}
	return ec, nil
}

// LoadOrCreateCA loads the CA at certPath/keyPath, generating and persisting a
// fresh one (valid for ttl) if either file is missing. This is the single entry
// point both `keydris init` and the daemon use so they share one stable root.
func LoadOrCreateCA(certPath, keyPath, commonName string, ttl time.Duration) (*CA, error) {
	ca, err := LoadCA(certPath, keyPath)
	if err == nil {
		return ca, nil
	}
	if !os.IsNotExist(err) {
		// A present-but-corrupt CA is a hard error: regenerating silently would
		// invalidate an already-installed root.
		if _, statErr := os.Stat(certPath); statErr == nil {
			return nil, fmt.Errorf("load existing CA: %w", err)
		}
	}
	ca, err = GenerateCA(commonName, ttl)
	if err != nil {
		return nil, err
	}
	if err := ca.Save(certPath, keyPath); err != nil {
		return nil, err
	}
	return ca, nil
}

// ServerTLSConfig returns a tls.Config that mints a leaf certificate per SNI
// host on demand (cached), so a single CONNECT proxy can terminate TLS for any
// upstream the agent dials.
func (c *CA) ServerTLSConfig() *tls.Config {
	cache := &leafCache{ca: c, m: map[string]*tls.Certificate{}}
	return &tls.Config{
		GetCertificate: cache.get,
		MinVersion:     tls.VersionTLS12,
	}
}

type leafCache struct {
	ca *CA
	mu sync.Mutex
	m  map[string]*tls.Certificate
}

func (lc *leafCache) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if host == "" {
		host = "localhost"
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if cert, ok := lc.m[host]; ok {
		return cert, nil
	}
	leaf, err := lc.ca.LeafFor(host)
	if err != nil {
		return nil, err
	}
	lc.m[host] = &leaf
	return &leaf, nil
}

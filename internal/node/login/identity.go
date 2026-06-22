package login

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// File names inside IdentityDir.
const (
	KeyFile    = "client.key"  // locally generated EC private key (never leaves the host)
	CertFile   = "client.crt"  // the control-plane-signed client certificate
	CAFile     = "ca.crt"      // the client CA to pin when presenting the cert
	WhoamiFile = "whoami.json" // human-readable identity metadata
)

// Identity is the metadata persisted alongside the key/cert so `keydris whoami`
// and `keydris status` can report who is logged in without parsing the cert.
type Identity struct {
	Email      string `json:"email"`
	Subject    string `json:"subject"`
	SPIFFEID   string `json:"spiffe_id"`
	NotAfter   string `json:"not_after"`
	ControlURL string `json:"control_url"`
	LoggedInAt string `json:"logged_in_at"`
}

// Expired reports whether the certificate's NotAfter has passed.
func (id *Identity) Expired() bool {
	t, err := time.Parse(time.RFC3339, id.NotAfter)
	if err != nil {
		return false
	}
	return time.Now().After(t)
}

// store writes the private key (0600), certificate, pinned CA, and metadata.
func store(dir string, id *Identity, key *ecdsa.PrivateKey, signed *signResponse) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, KeyFile), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, CertFile), []byte(signed.Certificate), 0o644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, CAFile), []byte(signed.CACert), 0o644); err != nil {
		return fmt.Errorf("write ca: %w", err)
	}
	meta, _ := json.MarshalIndent(id, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, WhoamiFile), append(meta, '\n'), 0o644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

// Load reads the persisted identity metadata, returning a clear error when the
// user has not logged in yet.
func Load(dir string) (*Identity, error) {
	b, err := os.ReadFile(filepath.Join(dir, WhoamiFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not logged in (run `keydris login`)")
		}
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, fmt.Errorf("parse identity: %w", err)
	}
	return &id, nil
}

// ClientTLSConfig loads the stored key/cert/CA into a tls.Config the daemon uses
// to authenticate to the control plane over mTLS. This is the "later used by the
// agent" half of the design: the daemon presents this identity on every call.
func ClientTLSConfig(dir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
	if err != nil {
		return nil, fmt.Errorf("load client keypair (run `keydris login`): %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no usable CA certs in %s", filepath.Join(dir, CAFile))
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      roots,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// HTTPClient builds an http.Client that authenticates to the control plane over
// mTLS using the stored login identity. It returns the actionable "run `keydris
// login`" error from ClientTLSConfig when no identity is present.
func HTTPClient(dir string, timeout time.Duration) (*http.Client, error) {
	tlsConf, err := ClientTLSConfig(dir)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsConf},
	}, nil
}

// Logout removes the stored identity material.
func Logout(dir string) error {
	var firstErr error
	for _, f := range []string{KeyFile, CertFile, CAFile, WhoamiFile} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

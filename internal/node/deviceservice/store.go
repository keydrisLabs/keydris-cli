package deviceservice

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	deviceKeyFile      = "device.key"
	deviceIdentityFile = "identity.json"
)

var ErrNotEnrolled = errors.New("device is not enrolled")

// Store persists root-only device state. Identity replacement is atomic; the
// key is created once and is never returned through the service API.
type Store struct {
	dir         string
	expectedUID uint32
	mu          sync.RWMutex
}

func NewStore(dir string, expectedUID uint32) *Store {
	return &Store{dir: dir, expectedUID: expectedUID}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Ensure() error {
	if s.dir == "" || !filepath.IsAbs(s.dir) {
		return fmt.Errorf("device service directory must be absolute")
	}
	if _, err := os.Lstat(s.dir); err == nil {
		// Validate before chmod so an attacker-controlled link is never followed.
		if err := validateOwnedPath(s.dir, s.expectedUID, true); err != nil {
			return err
		}
	} else if os.IsNotExist(err) {
		parent := filepath.Dir(s.dir)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create device service parent directory: %w", err)
		}
		if err := validateStateParent(parent, s.expectedUID); err != nil {
			return fmt.Errorf("validate device service parent directory: %w", err)
		}
		if err := os.Mkdir(s.dir, 0o700); err != nil {
			return fmt.Errorf("create device service directory: %w", err)
		}
		if err := os.Chmod(s.dir, 0o700); err != nil {
			return fmt.Errorf("secure device service directory: %w", err)
		}
		if err := validateOwnedPath(s.dir, s.expectedUID, true); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("inspect device service directory: %w", err)
	}
	_, err := s.signer()
	return err
}

func (s *Store) signer() (*ecdsa.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signerLocked()
}

func (s *Store) signerLocked() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(s.dir, deviceKeyFile)
	data, err := os.ReadFile(path)
	if err == nil {
		if err := validateOwnedPath(path, s.expectedUID, false); err != nil {
			return nil, err
		}
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "EC PRIVATE KEY" {
			return nil, fmt.Errorf("invalid device private key")
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse device private key: %w", err)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read device private key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device private key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return s.signerLocked()
		}
		return nil, fmt.Errorf("create device private key: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return key, nil
}

func (s *Store) PublicKeyPEM() ([]byte, error) {
	key, err := s.signer()
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func (s *Store) Identity() (*Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.dir, deviceIdentityFile)
	if err := validateOwnedPath(path, s.expectedUID, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotEnrolled
		}
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotEnrolled
		}
		return nil, err
	}
	defer file.Close()
	var identity Identity
	decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
	if err := decoder.Decode(&identity); err != nil {
		return nil, fmt.Errorf("parse device identity: %w", err)
	}
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	return &identity, nil
}

func (s *Store) InstallIdentity(identity Identity) error {
	if err := validateIdentity(identity); err != nil {
		return err
	}
	key, err := s.signer()
	if err != nil {
		return err
	}
	cert, err := parseCertificate(identity.CertificatePEM)
	if err != nil {
		return err
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.X.Cmp(key.PublicKey.X) != 0 || public.Y.Cmp(key.PublicKey.Y) != 0 {
		return fmt.Errorf("device certificate does not match the protected private key")
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(filepath.Join(s.dir, deviceIdentityFile), append(body, '\n'), 0o600)
}

func validateIdentity(identity Identity) error {
	if identity.DeviceID == "" || len(identity.DeviceID) > 512 {
		return fmt.Errorf("device identity has an invalid device id")
	}
	cert, err := parseCertificate(identity.CertificatePEM)
	if err != nil {
		return err
	}
	expiry, err := identity.Expiry()
	if err != nil {
		return err
	}
	if !expiry.Equal(cert.NotAfter.UTC()) {
		return fmt.Errorf("device certificate expiry does not match metadata")
	}
	now := time.Now()
	if now.Before(cert.NotBefore.Add(-5 * time.Minute)) {
		return fmt.Errorf("device certificate is not valid yet")
	}
	if !now.Before(cert.NotAfter) {
		return fmt.Errorf("device certificate is expired")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return fmt.Errorf("device certificate cannot be used for signing")
	}
	return nil
}

func parseCertificate(value string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid device certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse device certificate: %w", err)
	}
	return cert, nil
}

func writeAtomic(path string, body []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".device-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

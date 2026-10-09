package deviceservice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
	"time"
)

// FakeClient is an in-memory device service used by enrollment and user-bind
// tests. It exercises the same caller-bound signature format as the real IPC
// service without requiring root or a socket.
type FakeClient struct {
	mu       sync.RWMutex
	uid      uint32
	key      *ecdsa.PrivateKey
	identity *Identity
	started  time.Time
	RenewFn  func(context.Context, *Identity) (Identity, error)
}

var _ Client = (*FakeClient)(nil)

func NewFakeClient(uid uint32) (*FakeClient, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &FakeClient{uid: uid, key: key, started: time.Now().UTC()}, nil
}

func (f *FakeClient) Health(context.Context) (Health, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	value := Health{ProtocolVersion: ProtocolVersion, PID: os.Getpid(), CallerUID: f.uid, StartedAt: f.started}
	if f.identity != nil {
		value.Enrolled = true
		value.DeviceID = f.identity.DeviceID
		value.CertificateExpiry = f.identity.NotAfter
	}
	return value, nil
}

func (f *FakeClient) Identity(context.Context) (*Identity, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.identity == nil {
		return nil, ErrNotEnrolled
	}
	value := *f.identity
	return &value, nil
}

func (f *FakeClient) CreateCSR(_ context.Context, request CSRRequest) (CSR, error) {
	if f.uid != 0 {
		return CSR{}, fmt.Errorf("create device CSR requires root")
	}
	return createCSR(f.key, request.Subject)
}

func (f *FakeClient) Sign(_ context.Context, request SignRequest) (Signature, error) {
	digest, err := SignedDigest(request.Purpose, f.uid, request.Payload)
	if err != nil {
		return Signature{}, err
	}
	value, err := ecdsa.SignASN1(rand.Reader, f.key, digest)
	if err != nil {
		return Signature{}, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		return Signature{}, err
	}
	result := Signature{
		Algorithm: "ECDSA_P256_SHA256", Purpose: request.Purpose, CallerUID: f.uid,
		PayloadDigest: base64.RawURLEncoding.EncodeToString(digest),
		Signature:     base64.RawURLEncoding.EncodeToString(value),
		PublicKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
	}
	f.mu.RLock()
	if f.identity != nil {
		result.CertificatePEM = f.identity.CertificatePEM
	}
	f.mu.RUnlock()
	return result, nil
}

func (f *FakeClient) InstallIdentity(_ context.Context, identity Identity) error {
	if f.uid != 0 {
		return fmt.Errorf("install device identity requires root")
	}
	if err := validateIdentity(identity); err != nil {
		return err
	}
	cert, err := parseCertificate(identity.CertificatePEM)
	if err != nil {
		return err
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.X.Cmp(f.key.PublicKey.X) != 0 || public.Y.Cmp(f.key.PublicKey.Y) != 0 {
		return fmt.Errorf("device certificate does not match the protected private key")
	}
	f.mu.Lock()
	value := identity
	f.identity = &value
	f.mu.Unlock()
	return nil
}

func (f *FakeClient) Renew(ctx context.Context) (*Identity, error) {
	if f.uid != 0 {
		return nil, fmt.Errorf("renew device identity requires root")
	}
	if f.RenewFn == nil {
		return nil, fmt.Errorf("device renewal backend is not configured")
	}
	current, err := f.Identity(ctx)
	if err != nil {
		return nil, err
	}
	next, err := f.RenewFn(ctx, current)
	if err != nil {
		return nil, err
	}
	if err := f.InstallIdentity(ctx, next); err != nil {
		return nil, err
	}
	return &next, nil
}

// PublicKey returns the fake's public key so tests can issue matching device
// certificates without gaining access to the real service's private key.
func (f *FakeClient) PublicKey() *ecdsa.PublicKey { return &f.key.PublicKey }

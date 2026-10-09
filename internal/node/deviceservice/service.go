package deviceservice

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Caller struct {
	UID uint32
	PID int32
}

// RenewalRequest is the backend-facing contract point. Task 2 can adapt this
// interface to the agreed HTTP contract without changing the root IPC surface.
type RenewalRequest struct {
	Identity Identity  `json:"identity"`
	CSRPEM   string    `json:"csr_pem"`
	Proof    Signature `json:"proof"`
}

type Renewer interface {
	RenewDevice(context.Context, RenewalRequest) (Identity, error)
}

type Service struct {
	store     *Store
	renewer   Renewer
	startedAt time.Time
}

func NewService(store *Store, renewer Renewer) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("device store is required")
	}
	if err := store.Ensure(); err != nil {
		return nil, err
	}
	return &Service{store: store, renewer: renewer, startedAt: time.Now().UTC()}, nil
}

func (s *Service) Health(caller Caller) Health {
	health := Health{
		ProtocolVersion: ProtocolVersion,
		PID:             os.Getpid(),
		CallerUID:       caller.UID,
		StartedAt:       s.startedAt,
	}
	if identity, err := s.store.Identity(); err == nil {
		health.Enrolled = true
		health.DeviceID = identity.DeviceID
		health.CertificateExpiry = identity.NotAfter
	}
	return health
}

func (s *Service) Identity() (*Identity, error) { return s.store.Identity() }

func (s *Service) CreateCSR(caller Caller, request CSRRequest) (CSR, error) {
	if caller.UID != 0 {
		return CSR{}, fmt.Errorf("create device CSR requires root")
	}
	key, err := s.store.signer()
	if err != nil {
		return CSR{}, err
	}
	return createCSR(key, request.Subject)
}

func createCSR(key *ecdsa.PrivateKey, subject string) (CSR, error) {
	if len(subject) > 512 || strings.ContainsAny(subject, "\x00\r\n") {
		return CSR{}, fmt.Errorf("invalid device CSR subject")
	}
	request := &x509.CertificateRequest{}
	request.Subject.CommonName = subject
	der, err := x509.CreateCertificateRequest(rand.Reader, request, key)
	if err != nil {
		return CSR{}, fmt.Errorf("create device CSR: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return CSR{}, err
	}
	return CSR{
		PEM:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
		PublicKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
	}, nil
}

func (s *Service) Sign(caller Caller, request SignRequest) (Signature, error) {
	digest, err := SignedDigest(request.Purpose, caller.UID, request.Payload)
	if err != nil {
		return Signature{}, err
	}
	key, err := s.store.signer()
	if err != nil {
		return Signature{}, err
	}
	value, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		return Signature{}, fmt.Errorf("sign device proof: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return Signature{}, err
	}
	result := Signature{
		Algorithm:     "ECDSA_P256_SHA256",
		Purpose:       request.Purpose,
		CallerUID:     caller.UID,
		PayloadDigest: base64.RawURLEncoding.EncodeToString(digest),
		Signature:     base64.RawURLEncoding.EncodeToString(value),
		PublicKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
	}
	if identity, identityErr := s.store.Identity(); identityErr == nil {
		result.CertificatePEM = identity.CertificatePEM
	}
	return result, nil
}

func (s *Service) InstallIdentity(caller Caller, identity Identity) error {
	if caller.UID != 0 {
		return fmt.Errorf("install device identity requires root")
	}
	return s.store.InstallIdentity(identity)
}

func (s *Service) Renew(ctx context.Context, caller Caller) (*Identity, error) {
	if caller.UID != 0 {
		return nil, fmt.Errorf("renew device identity requires root")
	}
	if s.renewer == nil {
		return nil, fmt.Errorf("device renewal backend is not configured")
	}
	current, err := s.store.Identity()
	if err != nil {
		return nil, err
	}
	csr, err := s.CreateCSR(caller, CSRRequest{Subject: current.DeviceID})
	if err != nil {
		return nil, err
	}
	csrBlock, _ := pem.Decode([]byte(csr.PEM))
	proof, err := s.Sign(caller, SignRequest{Purpose: PurposeDeviceProof, Payload: csrBlock.Bytes})
	if err != nil {
		return nil, err
	}
	next, err := s.renewer.RenewDevice(ctx, RenewalRequest{
		Identity: *current,
		CSRPEM:   csr.PEM,
		Proof:    proof,
	})
	if err != nil {
		return nil, fmt.Errorf("renew device identity: %w", err)
	}
	if err := s.store.InstallIdentity(next); err != nil {
		return nil, fmt.Errorf("store renewed device identity: %w", err)
	}
	return &next, nil
}

func (s *Service) EnsureFresh(ctx context.Context, window time.Duration) error {
	identity, err := s.store.Identity()
	if errors.Is(err, ErrNotEnrolled) {
		return nil
	}
	if err != nil {
		return err
	}
	expiry, err := identity.Expiry()
	if err != nil {
		return err
	}
	if time.Now().Add(window).Before(expiry) {
		return nil
	}
	_, err = s.Renew(ctx, Caller{UID: 0})
	return err
}

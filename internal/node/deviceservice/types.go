// Package deviceservice owns the machine identity used by Keydris. The
// privileged service keeps the private key out of user processes and exposes a
// deliberately small, caller-bound local protocol.
package deviceservice

import (
	"context"
	"crypto"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

const ProtocolVersion = 1

const (
	PurposeDeviceProof = "device-proof"
	PurposeProxyCSR    = "proxy-csr"
	PurposeUserBind    = "user-bind"
)

// Identity is public device credential material. Private key bytes are never
// included in this type or returned by the service protocol.
type Identity struct {
	DeviceID         string `json:"device_id"`
	CertificatePEM   string `json:"certificate_pem"`
	CACertificatePEM string `json:"ca_certificate_pem,omitempty"`
	NotAfter         string `json:"not_after"`
	Revision         string `json:"revision,omitempty"`
}

func (i Identity) Expiry() (time.Time, error) {
	value, err := time.Parse(time.RFC3339, i.NotAfter)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid device certificate expiry: %w", err)
	}
	return value, nil
}

type Health struct {
	ProtocolVersion   int       `json:"protocol_version"`
	PID               int       `json:"pid"`
	Enrolled          bool      `json:"enrolled"`
	DeviceID          string    `json:"device_id,omitempty"`
	CertificateExpiry string    `json:"certificate_expiry,omitempty"`
	CallerUID         uint32    `json:"caller_uid"`
	StartedAt         time.Time `json:"started_at"`
}

type SignRequest struct {
	Purpose string `json:"purpose"`
	Payload []byte `json:"payload"`
}

type CSRRequest struct {
	Subject string `json:"subject,omitempty"`
}

type CSR struct {
	PEM          string `json:"pem"`
	PublicKeyPEM string `json:"public_key_pem"`
}

type Signature struct {
	Algorithm      string `json:"algorithm"`
	Purpose        string `json:"purpose"`
	CallerUID      uint32 `json:"caller_uid"`
	PayloadDigest  string `json:"payload_digest"`
	Signature      string `json:"signature"`
	CertificatePEM string `json:"certificate_pem,omitempty"`
	PublicKeyPEM   string `json:"public_key_pem"`
}

// SignedDigest returns the digest signed by the service. The purpose and
// caller UID are domain-separated so a signature from one local user or flow
// cannot be replayed as another.
func SignedDigest(purpose string, uid uint32, payload []byte) ([]byte, error) {
	if !validPurpose(purpose) {
		return nil, fmt.Errorf("unsupported signing purpose %q", purpose)
	}
	if len(payload) == 0 || len(payload) > 64<<10 {
		return nil, fmt.Errorf("signing payload must be between 1 byte and 64 KiB")
	}
	h := crypto.SHA256.New()
	h.Write([]byte("keydris-device-signature-v1\x00"))
	h.Write([]byte(purpose))
	h.Write([]byte{'\x00'})
	h.Write([]byte(strconv.FormatUint(uint64(uid), 10)))
	h.Write([]byte{'\x00'})
	h.Write(payload)
	return h.Sum(nil), nil
}

func validPurpose(value string) bool {
	switch value {
	case PurposeDeviceProof, PurposeProxyCSR, PurposeUserBind:
		return true
	default:
		return false
	}
}

func decodeSignature(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}

// Client is the interface used by enrollment and user-bind tasks. FakeClient
// lets those tasks land before launchd and the real control plane are wired.
type Client interface {
	Health(context.Context) (Health, error)
	Identity(context.Context) (*Identity, error)
	CreateCSR(context.Context, CSRRequest) (CSR, error)
	Sign(context.Context, SignRequest) (Signature, error)
	InstallIdentity(context.Context, Identity) error
	Renew(context.Context) (*Identity, error)
}

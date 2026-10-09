//go:build !windows

package deviceservice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func publicKeyFromStore(t *testing.T, store *Store) *ecdsa.PublicKey {
	t.Helper()
	value, err := store.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(value)
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.(*ecdsa.PublicKey)
}

func issueIdentity(t *testing.T, public *ecdsa.PublicKey, deviceID, revision string, expires time.Time) Identity {
	t.Helper()
	issuer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: deviceID},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     expires.UTC(),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, issuer)
	if err != nil {
		t.Fatal(err)
	}
	return Identity{
		DeviceID:       deviceID,
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		NotAfter:       expires.UTC().Format(time.RFC3339),
		Revision:       revision,
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir, uint32(os.Getuid()))
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStoreCreatesProtectedPersistentKey(t *testing.T) {
	store := newTestStore(t)
	first, err := store.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(store.Dir(), uint32(os.Getuid())).PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("device key changed after reopening the store")
	}
	info, err := os.Stat(filepath.Join(store.Dir(), deviceKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("device key mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStoreCreatesOnlyFinalStateDirectoryAsRootOnly(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "device")
	store := NewStore(dir, uint32(os.Getuid()))
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("device state directory mode = %o, want 700", info.Mode().Perm())
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if parentInfo.Mode().Perm() != 0o755 {
		t.Fatalf("parent directory mode changed to %o", parentInfo.Mode().Perm())
	}
}

func TestStoreInstallsOnlyCertificateMatchingProtectedKey(t *testing.T) {
	store := newTestStore(t)
	expires := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	identity := issueIdentity(t, publicKeyFromStore(t, store), "device-1", "v1", expires)
	if err := store.InstallIdentity(identity); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != "device-1" || loaded.Revision != "v1" {
		t.Fatalf("loaded identity = %+v", loaded)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InstallIdentity(issueIdentity(t, &other.PublicKey, "device-1", "v2", expires)); err == nil {
		t.Fatal("accepted a certificate for a different private key")
	}
}

func TestStoreRejectsRelaxedPrivateKeyPermissions(t *testing.T) {
	store := newTestStore(t)
	path := filepath.Join(store.Dir(), deviceKeyFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.Dir(), uint32(os.Getuid())).PublicKeyPEM(); err == nil {
		t.Fatal("accepted group-readable device private key")
	}
}

func TestStoreRejectsSymlinkStateDirectory(t *testing.T) {
	target := t.TempDir()
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "device")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(link, uint32(os.Getuid())).Ensure(); err == nil {
		t.Fatal("accepted a symlink as the protected state directory")
	}
}

func TestSignBindsPurposeAndKernelCallerUID(t *testing.T) {
	store := newTestStore(t)
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("bind this user")
	result, err := service.Sign(Caller{UID: 501}, SignRequest{Purpose: PurposeUserBind, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := SignedDigest(PurposeUserBind, 501, payload)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(result.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(publicKeyFromStore(t, store), digest, signature) {
		t.Fatal("device signature did not verify")
	}
	wrongCaller, _ := SignedDigest(PurposeUserBind, 502, payload)
	if ecdsa.VerifyASN1(publicKeyFromStore(t, store), wrongCaller, signature) {
		t.Fatal("device signature was valid for a different caller UID")
	}
}

func TestSocketUsesOperatingSystemPeerCredentials(t *testing.T) {
	store := newTestStore(t)
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	socketDir := t.TempDir()
	if err := os.Chmod(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "device.sock")
	server, err := Serve(socket, service, ServerOptions{ExpectedUID: uint32(os.Getuid())})
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("sandbox does not allow Unix sockets")
		}
		t.Fatal(err)
	}
	defer server.Close()
	client := SocketClient{Path: socket}
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.CallerUID != uint32(os.Getuid()) {
		t.Fatalf("caller uid = %d, want %d", health.CallerUID, os.Getuid())
	}
	result, err := client.Sign(context.Background(), SignRequest{Purpose: PurposeDeviceProof, Payload: []byte("challenge")})
	if err != nil {
		t.Fatal(err)
	}
	if result.CallerUID != uint32(os.Getuid()) {
		t.Fatalf("signature caller uid = %d", result.CallerUID)
	}
}

func TestNonRootCallerCannotInstallOrRenewIdentity(t *testing.T) {
	store := newTestStore(t)
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := issueIdentity(t, publicKeyFromStore(t, store), "device-1", "v1", time.Now().Add(time.Hour).Truncate(time.Second))
	if err := service.InstallIdentity(Caller{UID: 501}, identity); err == nil {
		t.Fatal("non-root caller installed a device identity")
	}
	if _, err := service.Renew(context.Background(), Caller{UID: 501}); err == nil {
		t.Fatal("non-root caller renewed a device identity")
	}
	if _, err := service.CreateCSR(Caller{UID: 501}, CSRRequest{}); err == nil {
		t.Fatal("non-root caller created a device CSR")
	}
}

func TestRootCreatesEnrollmentCSRWithoutExportingPrivateKey(t *testing.T) {
	store := newTestStore(t)
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.CreateCSR(Caller{UID: 0}, CSRRequest{Subject: "managed-enrollment"})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(value.PEM))
	if block == nil {
		t.Fatal("device service returned invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR signature: %v", err)
	}
	if csr.Subject.CommonName != "managed-enrollment" {
		t.Fatalf("CSR subject = %q", csr.Subject.CommonName)
	}
	if _, remainder := pem.Decode([]byte(value.PublicKeyPEM)); len(remainder) != 0 {
		t.Fatal("public key response contained unexpected data")
	}
}

type testRenewer struct {
	t        *testing.T
	store    *Store
	revision string
}

func (r testRenewer) RenewDevice(_ context.Context, request RenewalRequest) (Identity, error) {
	block, _ := pem.Decode([]byte(request.CSRPEM))
	if block == nil {
		r.t.Fatal("renewal request did not include a CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		r.t.Fatal(err)
	}
	public := csr.PublicKey.(*ecdsa.PublicKey)
	return issueIdentity(r.t, public, request.Identity.DeviceID, r.revision, time.Now().Add(48*time.Hour).Truncate(time.Second)), nil
}

func TestRenewalAtomicallyReplacesPublicIdentity(t *testing.T) {
	store := newTestStore(t)
	initial := issueIdentity(t, publicKeyFromStore(t, store), "device-1", "v1", time.Now().Add(time.Hour).Truncate(time.Second))
	if err := store.InstallIdentity(initial); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, testRenewer{t: t, store: store, revision: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := service.Renew(context.Background(), Caller{UID: 0})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Revision != "v2" {
		t.Fatalf("renewed revision = %q", renewed.Revision)
	}
	loaded, err := store.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != "v2" {
		t.Fatalf("stored revision = %q", loaded.Revision)
	}
}

func TestIdentityBeforeEnrollment(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Identity(); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("identity error = %v, want ErrNotEnrolled", err)
	}
}

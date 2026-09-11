package login

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentStoresMatchingDeviceIdentity(t *testing.T) {
	for _, browser := range []bool{false, true} {
		name := "access-token"
		if browser {
			name = "browser"
		}
		t.Run(name, func(t *testing.T) {
			caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ca := &x509.Certificate{
				SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
				NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
				IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
			}
			caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
			if err != nil {
				t.Fatal(err)
			}
			const token = "test-cognito-access-token"
			const agentID = "11111111-1111-4111-8111-111111111111"
			signs := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "id_token": "unused-id-token"})
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/identity/sign" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("unexpected enrollment request or bearer token")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if body["agent_id"] != agentID || body["device_name"] != "ci-run" || body["cli_version"] == "" {
					t.Error("missing agent binding or client metadata")
				}
				if signs > 0 && body["device_id"] != "device-123" {
					t.Error("re-enrollment did not retain the device ID")
				}
				signs++
				block, _ := pem.Decode([]byte(body["csr"]))
				if block == nil {
					t.Error("missing CSR")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				csr, err := x509.ParseCertificateRequest(block.Bytes)
				if err != nil || csr.CheckSignature() != nil {
					t.Error("invalid CSR or proof of private-key possession")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				cert := &x509.Certificate{
					SerialNumber: big.NewInt(int64(signs + 1)), Subject: pkix.Name{CommonName: "verified-user"},
					NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
					KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				}
				der, err := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, caKey)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(signResponse{
					Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
					CACert:      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
					Email:       "ci@example.com", DeviceID: "device-123", AgentID: agentID,
					NotAfter: cert.NotAfter.Format(time.RFC3339), SPIFFEID: "spiffe://test/user/ci",
				})
			}))
			defer server.Close()
			dir := t.TempDir()
			opt := Options{ControlURL: server.URL, IdentityDir: dir, AgentID: agentID, DeviceName: "ci-run", Timeout: time.Second}
			opt.Open = func(raw string) error {
				u, err := url.Parse(raw)
				if err != nil {
					return err
				}
				q := u.Query()
				callback := q.Get("redirect_uri") + "?" + url.Values{"code": {"test-code"}, "state": {q.Get("state")}}.Encode()
				response, err := http.Get(callback)
				if err == nil {
					response.Body.Close()
				}
				return err
			}
			for attempt := 0; attempt < 2; attempt++ {
				var id *Identity
				if browser {
					id, err = Run(opt)
				} else {
					id, err = EnrollWithAccessToken(opt, token+"\n")
				}
				if err != nil {
					t.Fatal(err)
				}
				if id.AgentID != agentID || id.Email != "ci@example.com" {
					t.Fatalf("wrong server-derived identity: %+v", id)
				}
				if _, err := tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile)); err != nil {
					t.Fatalf("stored certificate does not match generated private key: %v", err)
				}
			}
			info, err := os.Stat(filepath.Join(dir, KeyFile))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("private key must be stored with mode 0600")
			}
			for _, file := range []string{KeyFile, CertFile, CAFile, WhoamiFile} {
				body, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil || bytes.Contains(body, []byte(token)) {
					t.Fatalf("identity file %s is unreadable or contains the bearer token", file)
				}
			}
		})
	}
}

func TestRejectedAccessTokenPreservesIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "access token rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, KeyFile)
	if err := os.WriteFile(keyPath, []byte("existing-private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnrollWithAccessToken(Options{ControlURL: server.URL, IdentityDir: dir}, "rejected-token"); err == nil {
		t.Fatal("rejected enrollment succeeded")
	}
	body, err := os.ReadFile(keyPath)
	if err != nil || string(body) != "existing-private-key" {
		t.Fatal("failed enrollment replaced the existing private key")
	}
}

func TestAccessTokenEnrollmentRejectsInvalidInput(t *testing.T) {
	t.Setenv("KEYDRIS_ALLOW_INSECURE_CONTROL", "")
	for _, token := range []string{"", " \n", "Bearer token", "first\nsecond"} {
		if _, err := EnrollWithAccessToken(Options{}, token); err == nil {
			t.Error("accepted an empty or multi-part token")
		}
	}
	_, err := EnrollWithAccessToken(Options{ControlURL: "http://control.example.com", IdentityDir: t.TempDir()}, "token")
	if err == nil || !strings.Contains(err.Error(), "use https") {
		t.Fatalf("plaintext enrollment must be refused: %v", err)
	}
}

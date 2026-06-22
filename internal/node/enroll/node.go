// Package enroll handles node onboarding for the POC: a user obtains an
// enrollment token (`keydris login`) and a root operator exchanges it for a
// persistent node credential (`keydris enroll`). In a real build the token
// would be validated by the control plane and the node credential would be a
// SPIFFE SVID for the node itself; here both are local files so the onboarding
// flow and file layout are exercised end-to-end.
package enroll

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// EnrollmentFile is the user-side token written by `keydris login`.
const EnrollmentFile = "enrollment.token"

// NodeCredentialFile is the root-side node credential written by `keydris enroll`.
const NodeCredentialFile = "node.json"

// NodeCredential identifies an enrolled node.
type NodeCredential struct {
	NodeID     string `json:"node_id"`
	Token      string `json:"token"`
	EnrolledAt string `json:"enrolled_at"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Login writes an enrollment token to dir (creating one if token == "").
func Login(dir, token string) (string, error) {
	if token == "" {
		token = randomHex(16)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, EnrollmentFile)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// LoadToken reads the enrollment token from dir.
func LoadToken(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, EnrollmentFile))
	if err != nil {
		return "", fmt.Errorf("no enrollment token (run `keydris login` first): %w", err)
	}
	return string(b[:len(b)-1]), nil
}

// Enroll exchanges the enrollment token for a node credential persisted to dir
// (0600). A fresh token is minted when token is empty. Returns the credential.
func Enroll(dir, token string) (*NodeCredential, error) {
	if token == "" {
		token = randomHex(16)
	}
	cred := &NodeCredential{
		NodeID:     "node-" + randomHex(8),
		Token:      token,
		EnrolledAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(cred, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, NodeCredentialFile), append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	return cred, nil
}

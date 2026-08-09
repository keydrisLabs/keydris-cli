package login

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EnsureFresh renews a device certificate before it enters the renewal window.
// The existing certificate authenticates the request; a new private key is
// generated locally and replaces the old key only after the server signs it.
func EnsureFresh(identityDir, mtlsControlURL, serverCAPath string, window time.Duration) (*Identity, error) {
	id, err := Load(identityDir)
	if err != nil {
		return nil, err
	}
	if !id.ExpiresWithin(window) {
		return id, nil
	}
	if id.Expired() {
		return nil, fmt.Errorf("client certificate is expired; re-enroll or run `keydris login`")
	}

	client, err := HTTPClient(identityDir, serverCAPath, 15*time.Second)
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csrPEM, err := makeCSR(key, id.Email)
	if err != nil {
		return nil, fmt.Errorf("build renewal CSR: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"csr": string(csrPEM)})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(mtlsControlURL, "/")+"/identity/renew",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("renew certificate: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf(
			"renew endpoint %s: %s",
			resp.Status,
			bytes.TrimSpace(responseBody),
		)
	}
	var signed signResponse
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil {
		return nil, err
	}
	renewed := &Identity{
		Email:      signed.Email,
		Subject:    signed.Subject,
		SPIFFEID:   signed.SPIFFEID,
		DeviceID:   signed.DeviceID,
		AgentID:    signed.AgentID,
		NotAfter:   signed.NotAfter,
		ControlURL: id.ControlURL,
		LoggedInAt: id.LoggedInAt,
	}
	if err := store(identityDir, renewed, key, &signed); err != nil {
		return nil, fmt.Errorf("store renewed identity: %w", err)
	}
	return renewed, nil
}

// Package authz defines the /authorize wire contract shared by the node proxy
// and the control plane: the request/response shapes and the client the proxy
// uses to consult the broker.
//
// It is deliberately dependency-free so both sides (the node daemon and the
// control-plane broker server) can depend on the contract without depending on
// each other.
package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Decision values returned by the broker.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// AuthorizeRequest is sent by the proxy for every intercepted connection.
//
// SVID is the per-session JWT-SVID resolved by attribution (empty on the
// proxy-env fallback, where the broker degrades to destination-only policy).
// SessionID carries the resolved SPIFFE ID for logging.
type AuthorizeRequest struct {
	DstAddr   string `json:"dst_addr"`
	DstHost   string `json:"dst_host,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	SVID      string `json:"svid,omitempty"`
	// PolicyID names the governance policy the broker should evaluate this
	// request against (set by `keydris init claude-code <policy-id>`).
	PolicyID string `json:"policy_id,omitempty"`
}

// Inject describes a credential the proxy should add to the request on the wire.
// Phase 1 only supports Type == "header".
type Inject struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AuthorizeResponse is the broker's decision.
type AuthorizeResponse struct {
	Decision string  `json:"decision"`
	Inject   *Inject `json:"inject,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

// Authorize is the client the proxy uses to consult the control plane. The
// client is supplied by the caller so it can present the daemon's mTLS identity
// (the certificate `keydris login` stored); baseURL is the mTLS control URL.
func Authorize(ctx context.Context, client *http.Client, baseURL string, req AuthorizeRequest) (*AuthorizeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/authorize", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("broker returned %s: %s", resp.Status, bytes.TrimSpace(b))
	}

	var out AuthorizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode broker response: %w", err)
	}
	return &out, nil
}

// Package authz defines the /agent/authorize wire contract shared by the node proxy
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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
	// ToolCall/ToolParams carry the MCP tool name and arguments when the
	// intercepted request is JSON-RPC tools/call; other JSON requests use the
	// HTTP "METHOD /path" and full request body fallback.
	ToolCall   string          `json:"tool_call,omitempty"`
	ToolParams json.RawMessage `json:"tool_params,omitempty"`
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

// BrokerHTTPError distinguishes control-plane/ingress failures from policy
// denials, which are successful HTTP 200 responses with decision == "deny".
type BrokerHTTPError struct {
	StatusCode  int
	Status      string
	ContentType string
	IsHTML      bool
}

func (e *BrokerHTTPError) Error() string {
	message := fmt.Sprintf("broker returned %s", e.Status)
	if e.ContentType != "" {
		message += " (" + e.ContentType + ")"
	}
	if e.IsHTML {
		message += "; check KEYDRIS_CONTROL_MTLS_URL, mTLS identity, and ingress/WAF"
	}
	return message
}

// ErrorKind returns a stable audit category without exposing response bodies.
func ErrorKind(err error) string {
	var httpErr *BrokerHTTPError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &httpErr):
		return "broker_http"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "context"
	default:
		var netErr net.Error
		if errors.As(err, &netErr) {
			return "transport"
		}
		if strings.Contains(err.Error(), "decode broker response") {
			return "decode"
		}
		return "broker"
	}
}

// Authorize is the client the proxy uses to consult the control plane. The
// client is supplied by the caller so it can present the daemon's mTLS identity
// (the certificate `keydris login` stored); baseURL is the mTLS control URL.
func Authorize(ctx context.Context, client *http.Client, baseURL string, req AuthorizeRequest) (*AuthorizeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/agent/authorize", bytes.NewReader(body))
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		contentType := resp.Header.Get("Content-Type")
		trimmed := bytes.TrimSpace(b)
		return nil, &BrokerHTTPError{
			StatusCode:  resp.StatusCode,
			Status:      resp.Status,
			ContentType: contentType,
			IsHTML:      strings.Contains(strings.ToLower(contentType), "text/html") || bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<html")),
		}
	}

	var out AuthorizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode broker response: %w", err)
	}
	return &out, nil
}

package runtimecontract

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

type MCPActionResource struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	ExternalID   string `json:"external_id"`
}

type MCPActionIntent struct {
	Provider     string            `json:"provider"`
	ConnectionID string            `json:"connection_id"`
	ActionType   string            `json:"action_type"`
	ActionName   string            `json:"action_name,omitempty"`
	Resource     MCPActionResource `json:"resource"`
	Parameters   map[string]any    `json:"parameters"`
}

type MintKitActionTokenRequest struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	RequestHash   string          `json:"request_hash"`
	Intent        MCPActionIntent `json:"intent"`
}

type KitActionTokenResponse struct {
	SchemaVersion  int    `json:"schema_version"`
	KitActionToken string `json:"kit_action_token"`
	ExpiresAt      string `json:"expires_at"`
}

func NewMintKitActionTokenRequest(
	requestID string,
	intent MCPActionIntent,
) (MintKitActionTokenRequest, error) {
	raw, err := json.Marshal(intent)
	if err != nil {
		return MintKitActionTokenRequest{}, fmt.Errorf(
			"encode MCP action intent: %w",
			err,
		)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return MintKitActionTokenRequest{}, fmt.Errorf(
			"canonicalize MCP action intent: %w",
			err,
		)
	}
	digest := sha256.Sum256(canonical)
	return MintKitActionTokenRequest{
		SchemaVersion: SchemaVersion,
		RequestID:     requestID,
		RequestHash:   fmt.Sprintf("sha256:%x", digest),
		Intent:        intent,
	}, nil
}

func MintKitActionToken(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, endpointPath string,
	input MintKitActionTokenRequest,
) (*KitActionTokenResponse, error) {
	if runtimeToken == "" {
		return nil, fmt.Errorf("KIT action token mint requires a session token")
	}
	raw, err := executeRuntimeJSON(
		ctx,
		client,
		baseURL,
		runtimeToken,
		endpointPath,
		input,
		"KIT action token mint",
	)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var result KitActionTokenResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, err
	}
	if result.SchemaVersion != SchemaVersion ||
		result.KitActionToken == "" ||
		len(result.KitActionToken) > 1024*1024 {
		return nil, fmt.Errorf("KIT action token response is invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) {
		return nil, fmt.Errorf("KIT action token response has invalid expiry")
	}
	return &result, nil
}

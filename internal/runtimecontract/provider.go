package runtimecontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type ProviderRequestHeaders struct {
	Accept           string `json:"accept,omitempty"`
	IfMatch          string `json:"if_match,omitempty"`
	IfNoneMatch      string `json:"if_none_match,omitempty"`
	GithubAPIVersion string `json:"github_api_version,omitempty"`
}

type ProviderHTTPRequest struct {
	Method  string                 `json:"method"`
	Path    string                 `json:"path"`
	Query   map[string][]string    `json:"query"`
	Headers ProviderRequestHeaders `json:"headers"`
	Body    map[string]any         `json:"body,omitempty"`
}

type ProviderExecutionRequest struct {
	SchemaVersion int                 `json:"schema_version"`
	RequestID     string              `json:"request_id"`
	ConnectionID  string              `json:"connection_id"`
	ResourceID    string              `json:"resource_id"`
	Request       ProviderHTTPRequest `json:"request"`
}

type RuntimeDecision struct {
	SchemaVersion int    `json:"schema_version"`
	DecisionID    string `json:"decision_id"`
	RequestID     string `json:"request_id"`
	AttemptID     string `json:"attempt_id"`
	CorrelationID string `json:"correlation_id"`
	DecidedAt     string `json:"decided_at"`
	Decision      string `json:"decision"`
	ReasonCode    string `json:"reason_code"`
	Obligations   []any  `json:"obligations"`
}

type ProviderHTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type ProviderExecutionResponse struct {
	SchemaVersion    int                   `json:"schema_version"`
	RequestID        string                `json:"request_id"`
	Decision         RuntimeDecision       `json:"decision"`
	ExecutionStatus  string                `json:"execution_status"`
	Replayed         bool                  `json:"replayed"`
	ErrorCode        *string               `json:"error_code"`
	ProviderResponse *ProviderHTTPResponse `json:"provider_response"`
}

func ExecuteProvider(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, endpointPath string,
	input ProviderExecutionRequest,
) (*ProviderExecutionResponse, error) {
	if runtimeToken == "" {
		return nil, fmt.Errorf("provider execution requires a session token")
	}
	raw, err := executeRuntimeJSON(
		ctx,
		client,
		baseURL,
		runtimeToken,
		endpointPath,
		input,
		"provider execution",
	)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var result ProviderExecutionResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, err
	}
	if err := result.validate(input.RequestID); err != nil {
		return nil, err
	}
	return &result, nil
}

func executeRuntimeJSON(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, endpointPath string,
	input any,
	label string,
) ([]byte, error) {
	endpoint, err := trustedRuntimeURL(baseURL, endpointPath)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", label, err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+runtimeToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute %s request: %w", label, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf(
			"%s returned %s: %s",
			label,
			response.Status,
			bytes.TrimSpace(responseBody),
		)
	}
	return readBounded(response.Body, maxResponseBytes, label+" response")
}

func (response ProviderExecutionResponse) validate(requestID string) error {
	if response.SchemaVersion != SchemaVersion ||
		response.RequestID != requestID ||
		response.Decision.SchemaVersion != SchemaVersion ||
		response.Decision.RequestID != requestID {
		return fmt.Errorf("provider execution response has invalid identity")
	}
	switch response.Decision.Decision {
	case "allow", "deny", "approval_required":
	default:
		return fmt.Errorf("provider execution response has invalid decision")
	}
	switch response.ExecutionStatus {
	case "denied":
		if response.Decision.Decision == "allow" ||
			response.ProviderResponse != nil ||
			response.ErrorCode != nil {
			return fmt.Errorf("provider denial response is inconsistent")
		}
	case "succeeded":
		if response.Decision.Decision != "allow" ||
			response.ProviderResponse == nil ||
			response.ErrorCode != nil {
			return fmt.Errorf("provider success response is inconsistent")
		}
	case "failed":
		if response.Decision.Decision != "allow" ||
			(response.ProviderResponse == nil && response.ErrorCode == nil) {
			return fmt.Errorf("provider failure response is inconsistent")
		}
	case "unknown":
		if response.Decision.Decision != "allow" ||
			response.ProviderResponse != nil || response.ErrorCode == nil {
			return fmt.Errorf("provider unknown response is inconsistent")
		}
	default:
		return fmt.Errorf("provider execution response has invalid status")
	}
	if response.ProviderResponse != nil &&
		(response.ProviderResponse.Status < 100 ||
			response.ProviderResponse.Status > 599) {
		return fmt.Errorf("provider execution response has invalid HTTP status")
	}
	return nil
}

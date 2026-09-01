package runtimecontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type MCPGatewayParams struct {
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	URI       string         `json:"uri,omitempty"`
}

type MCPGatewayMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Method  string           `json:"method"`
	Params  MCPGatewayParams `json:"params"`
}

type MCPGatewayRequest struct {
	SchemaVersion int               `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	ConnectionID  string            `json:"connection_id"`
	ResourceID    string            `json:"resource_id"`
	Message       MCPGatewayMessage `json:"message"`
}

type MCPJSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type MCPJSONRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *MCPJSONRPCError `json:"error,omitempty"`
}

type MCPGatewayResponse struct {
	SchemaVersion   int                 `json:"schema_version"`
	RequestID       string              `json:"request_id"`
	Decision        RuntimeDecision     `json:"decision"`
	ExecutionStatus string              `json:"execution_status"`
	Replayed        bool                `json:"replayed"`
	ErrorCode       *string             `json:"error_code"`
	MCPResponse     *MCPJSONRPCResponse `json:"mcp_response"`
}

func ExecuteMCPGateway(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, endpointPath string,
	input MCPGatewayRequest,
) (*MCPGatewayResponse, error) {
	if runtimeToken == "" {
		return nil, fmt.Errorf("MCP gateway requires a session token")
	}
	raw, err := executeRuntimeJSON(
		ctx,
		client,
		baseURL,
		runtimeToken,
		endpointPath,
		input,
		"MCP gateway",
	)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var result MCPGatewayResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, err
	}
	if err := result.validate(input.RequestID, input.Message.ID); err != nil {
		return nil, err
	}
	return &result, nil
}

func (response MCPGatewayResponse) validate(
	requestID string,
	jsonRPCID json.RawMessage,
) error {
	if response.SchemaVersion != SchemaVersion ||
		response.RequestID != requestID ||
		response.Decision.SchemaVersion != SchemaVersion ||
		response.Decision.RequestID != requestID {
		return fmt.Errorf("MCP gateway response has invalid identity")
	}
	switch response.Decision.Decision {
	case "allow", "deny", "approval_required":
	default:
		return fmt.Errorf("MCP gateway response has invalid decision")
	}
	switch response.ExecutionStatus {
	case "denied":
		if response.Decision.Decision == "allow" ||
			response.MCPResponse != nil ||
			response.ErrorCode != nil {
			return fmt.Errorf("MCP gateway denial response is inconsistent")
		}
	case "succeeded":
		if response.Decision.Decision != "allow" ||
			response.MCPResponse == nil ||
			response.MCPResponse.Error != nil ||
			response.ErrorCode != nil {
			return fmt.Errorf("MCP gateway success response is inconsistent")
		}
	case "failed":
		if response.Decision.Decision != "allow" ||
			(response.MCPResponse == nil && response.ErrorCode == nil) {
			return fmt.Errorf("MCP gateway failure response is inconsistent")
		}
	case "unknown":
		if response.Decision.Decision != "allow" ||
			response.MCPResponse != nil ||
			response.ErrorCode == nil {
			return fmt.Errorf("MCP gateway unknown response is inconsistent")
		}
	default:
		return fmt.Errorf("MCP gateway response has invalid status")
	}
	if response.MCPResponse != nil {
		if err := response.MCPResponse.validate(jsonRPCID); err != nil {
			return err
		}
	}
	return nil
}

func (response MCPJSONRPCResponse) validate(requestID json.RawMessage) error {
	hasResult := len(bytes.TrimSpace(response.Result)) > 0
	hasError := response.Error != nil
	if response.JSONRPC != "2.0" ||
		!bytes.Equal(bytes.TrimSpace(response.ID), bytes.TrimSpace(requestID)) ||
		hasResult == hasError {
		return fmt.Errorf("MCP gateway returned an invalid JSON-RPC response")
	}
	if hasResult && !json.Valid(response.Result) {
		return fmt.Errorf("MCP gateway returned an invalid JSON-RPC result")
	}
	if hasError && response.Error.Message == "" {
		return fmt.Errorf("MCP gateway returned an invalid JSON-RPC error")
	}
	return nil
}

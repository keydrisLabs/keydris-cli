package runtimecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// MCP session relay — handshake and inventory listing for a server whose
// upstream needs auth. Separate from the gateway because none of these methods
// is an action: no decision, no resource, nothing to record.

// MCPSessionMethods mirrors MCP_SESSION_METHODS in @keydris/contracts.
var MCPSessionMethods = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"ping":                      true,
	// Draft handshake; clients try it before `initialize`.
	"server/discover": true,
	"tools/list":      true,
	"resources/list":  true,
	"prompts/list":    true,
}

// IsMCPSessionMethod reports whether the relay accepts a JSON-RPC method.
func IsMCPSessionMethod(method string) bool {
	return MCPSessionMethods[method]
}

type MCPSessionMessage struct {
	JSONRPC string `json:"jsonrpc"`
	// Omitted for notifications.
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type MCPSessionRequest struct {
	SchemaVersion int               `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	ConnectionID  string            `json:"connection_id"`
	Message       MCPSessionMessage `json:"message"`
}

type MCPSessionResponse struct {
	SchemaVersion int                 `json:"schema_version"`
	RequestID     string              `json:"request_id"`
	Status        string              `json:"status"`
	ErrorCode     *string             `json:"error_code"`
	MCPResponse   *MCPJSONRPCResponse `json:"mcp_response"`
}

// RelayMCPSession relays one handshake or inventory call through Keydris.
func RelayMCPSession(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, endpointPath string,
	input MCPSessionRequest,
) (*MCPSessionResponse, error) {
	if runtimeToken == "" {
		return nil, fmt.Errorf("MCP session relay requires a session token")
	}
	raw, err := executeRuntimeJSON(
		ctx,
		client,
		baseURL,
		runtimeToken,
		endpointPath,
		input,
		"MCP session relay",
	)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var result MCPSessionResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, err
	}
	if err := result.validate(input.RequestID, input.Message.ID); err != nil {
		return nil, err
	}
	return &result, nil
}

func (response MCPSessionResponse) validate(
	requestID string,
	jsonRPCID json.RawMessage,
) error {
	if response.SchemaVersion != SchemaVersion ||
		response.RequestID != requestID {
		return fmt.Errorf("MCP session response has invalid identity")
	}
	switch response.Status {
	case "succeeded":
		if response.MCPResponse == nil || response.ErrorCode != nil {
			return fmt.Errorf("MCP session success response is inconsistent")
		}
	case "accepted":
		if response.MCPResponse != nil {
			return fmt.Errorf("MCP session accepted response is inconsistent")
		}
	case "failed":
		if response.ErrorCode == nil {
			return fmt.Errorf("MCP session failure response is inconsistent")
		}
	default:
		return fmt.Errorf("MCP session response has invalid status")
	}
	if response.MCPResponse != nil {
		if err := response.MCPResponse.validate(jsonRPCID); err != nil {
			return err
		}
	}
	return nil
}

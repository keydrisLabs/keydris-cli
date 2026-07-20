package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/authz"
	"github.com/keydrisLabs/keydris-cli/internal/evidence"
)

type authorizeAudit struct {
	DstAddr      string          `json:"dst_addr"`
	DstHost      string          `json:"dst_host,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PolicyID     string          `json:"policy_id,omitempty"`
	ToolCall     string          `json:"tool_call,omitempty"`
	ToolParams   json.RawMessage `json:"tool_params,omitempty"`
	Decision     string          `json:"decision"`
	Reason       string          `json:"reason,omitempty"`
	ErrorKind    string          `json:"error_kind,omitempty"`
	Error        string          `json:"error,omitempty"`
	LatencyMS    int64           `json:"latency_ms"`
	InjectHeader string          `json:"inject_header,omitempty"`
}

func appendAuthorizeAudit(ledger *evidence.Ledger, req authz.AuthorizeRequest, resp *authz.AuthorizeResponse, authErr error, elapsed time.Duration) error {
	if ledger == nil {
		return nil
	}
	entry := authorizeAudit{
		DstAddr:    req.DstAddr,
		DstHost:    req.DstHost,
		SessionID:  req.SessionID,
		PolicyID:   req.PolicyID,
		ToolCall:   req.ToolCall,
		ToolParams: sanitizeToolParams(req.ToolParams, req, resp),
		LatencyMS:  elapsed.Milliseconds(),
	}
	if authErr != nil {
		entry.Decision = "error"
		entry.ErrorKind = authz.ErrorKind(authErr)
		entry.Error = sanitizeAuthorizeText(authErr.Error(), req, resp)
	} else if resp != nil {
		entry.Decision = resp.Decision
		entry.Reason = sanitizeAuthorizeText(resp.Reason, req, resp)
		if resp.Inject != nil {
			entry.InjectHeader = resp.Inject.Name
		}
	} else {
		entry.Decision = "error"
		entry.ErrorKind = "invalid_response"
		entry.Error = "broker returned no response"
	}
	_, err := ledger.Append("authorize", entry)
	return err
}

func toolParamsForLog(params json.RawMessage) string {
	if len(params) == 0 {
		return "{}"
	}
	return fmt.Sprintf("%s", params)
}

func sanitizeAuthorizeText(value string, req authz.AuthorizeRequest, resp *authz.AuthorizeResponse) string {
	secrets := []string{req.SVID}
	if resp != nil && resp.Inject != nil {
		secrets = append(secrets, resp.Inject.Value)
	}
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func sanitizeToolParams(params json.RawMessage, req authz.AuthorizeRequest, resp *authz.AuthorizeResponse) json.RawMessage {
	if len(params) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	value = redactJSONStrings(value, req, resp)
	redacted, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return redacted
}

func redactJSONStrings(value any, req authz.AuthorizeRequest, resp *authz.AuthorizeResponse) any {
	switch typed := value.(type) {
	case string:
		return sanitizeAuthorizeText(typed, req, resp)
	case []any:
		for i := range typed {
			typed[i] = redactJSONStrings(typed[i], req, resp)
		}
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			redacted[sanitizeAuthorizeText(key, req, resp)] = redactJSONStrings(item, req, resp)
		}
		return redacted
	}
	return value
}

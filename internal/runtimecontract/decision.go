package runtimecontract

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
)

const maxDecisionResponseBytes = 64 * 1024

var (
	decisionIDPattern = regexp.MustCompile(`^Keydris-[0-9A-HJKMNP-TV-Z]{26}$`)
	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type NormalizedDecision string

const (
	DecisionAllow            NormalizedDecision = "allow"
	DecisionDeny             NormalizedDecision = "deny"
	DecisionApprovalRequired NormalizedDecision = "approval_required"
)

// DecisionResponse is the frozen runtime v1 authorization response. Keeping
// this strict in the CLI prevents backend/CLI enum drift from silently turning
// a new decision into an unintended permission outcome.
type DecisionResponse struct {
	SchemaVersion int                `json:"schema_version"`
	DecisionID    string             `json:"decision_id"`
	RequestID     string             `json:"request_id"`
	AttemptID     string             `json:"attempt_id"`
	CorrelationID string             `json:"correlation_id"`
	DecidedAt     string             `json:"decided_at"`
	Obligations   []json.RawMessage  `json:"obligations"`
	Decision      NormalizedDecision `json:"decision"`
	ReasonCode    string             `json:"reason_code"`
}

func DecodeDecisionResponse(reader io.Reader) (*DecisionResponse, error) {
	raw, err := readBounded(reader, maxDecisionResponseBytes, "decision response")
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var response DecisionResponse
	if err := decodeStrict(raw, &response); err != nil {
		return nil, fmt.Errorf("decode decision response: %w", err)
	}
	if err := response.Validate(); err != nil {
		return nil, err
	}
	return &response, nil
}

func (response DecisionResponse) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf(
			"unsupported runtime schema_version %d (expected %d)",
			response.SchemaVersion,
			SchemaVersion,
		)
	}
	if !decisionIDPattern.MatchString(response.DecisionID) {
		return fmt.Errorf("decision response has an invalid decision_id")
	}
	if !requestIDPattern.MatchString(response.RequestID) {
		return fmt.Errorf("decision response has an invalid request_id")
	}
	if !ulidPattern.MatchString(response.AttemptID) {
		return fmt.Errorf("decision response has an invalid attempt_id")
	}
	if !ulidPattern.MatchString(response.CorrelationID) {
		return fmt.Errorf("decision response has an invalid correlation_id")
	}
	if _, err := time.Parse(time.RFC3339, response.DecidedAt); err != nil {
		return fmt.Errorf("decision response has an invalid decided_at: %w", err)
	}
	if response.Obligations == nil || len(response.Obligations) != 0 {
		return fmt.Errorf("decision response obligations must be an empty array")
	}

	validReason := false
	switch response.Decision {
	case DecisionAllow:
		validReason = response.ReasonCode == "keydris_policy_allowed" ||
			response.ReasonCode == "keydris_approval_granted"
	case DecisionDeny:
		switch response.ReasonCode {
		case "keydris_policy_denied",
			"keydris_policy_unavailable",
			"keydris_invalid_request",
			"keydris_identity_unavailable",
			"keydris_target_unavailable",
			"keydris_action_unsupported",
			"keydris_manifest_stale",
			"keydris_request_conflict",
			"keydris_kit_action_token_invalid",
			"keydris_enforcement_unavailable":
			validReason = true
		}
	case DecisionApprovalRequired:
		validReason = response.ReasonCode == "keydris_approval_required"
	default:
		return fmt.Errorf("decision response has unsupported decision %q", response.Decision)
	}
	if !validReason {
		return fmt.Errorf(
			"decision response reason_code %q is invalid for %q",
			response.ReasonCode,
			response.Decision,
		)
	}
	return nil
}

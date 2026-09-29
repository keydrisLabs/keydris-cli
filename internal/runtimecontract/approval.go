package runtimecontract

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	ApprovalStatusEndpointPath = "/v1/runtime/approvals/status"
	approvalPollInterval       = time.Second
)

type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalApproved  ApprovalStatus = "approved"
	ApprovalRejected  ApprovalStatus = "rejected"
	ApprovalExpired   ApprovalStatus = "expired"
	ApprovalConsumed  ApprovalStatus = "consumed"
	ApprovalCancelled ApprovalStatus = "cancelled"
)

// ApprovalStatusResponse is the runtime-visible state for an action that was
// paused by an approval_required decision.
type ApprovalStatusResponse struct {
	ApprovalID string         `json:"approval_id"`
	RequestID  string         `json:"request_id"`
	Status     ApprovalStatus `json:"status"`
	ExpiresAt  string         `json:"expires_at"`
	ResolvedAt *string        `json:"resolved_at"`
}

// GetApprovalStatus reads and strictly validates one approval status.
func GetApprovalStatus(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, requestID string,
) (*ApprovalStatusResponse, error) {
	if runtimeToken == "" {
		return nil, fmt.Errorf("approval status requires a session token")
	}
	if !requestIDPattern.MatchString(requestID) {
		return nil, fmt.Errorf("approval status requires a valid request id")
	}
	endpoint, err := trustedRuntimeURL(baseURL, ApprovalStatusEndpointPath)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("request_id", requestID)
	parsed.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+runtimeToken)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch approval status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("approval status returned %s", response.Status)
	}
	raw, err := readBounded(response.Body, maxResponseBytes, "approval status")
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var result ApprovalStatusResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, fmt.Errorf("decode approval status: %w", err)
	}
	if err := result.validate(requestID); err != nil {
		return nil, err
	}
	return &result, nil
}

// WaitForApproval polls until the user acts in the Keydris console or the
// caller's context expires. Only an approved status permits a retry.
func WaitForApproval(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken, requestID string,
) error {
	for {
		status, err := GetApprovalStatus(ctx, client, baseURL, runtimeToken, requestID)
		if err != nil {
			return err
		}
		switch status.Status {
		case ApprovalApproved:
			return nil
		case ApprovalPending:
		case ApprovalRejected, ApprovalExpired, ApprovalConsumed, ApprovalCancelled:
			return fmt.Errorf("approval %s", status.Status)
		default:
			return fmt.Errorf("approval returned unsupported status %q", status.Status)
		}

		timer := time.NewTimer(approvalPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("wait for approval: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (response ApprovalStatusResponse) validate(requestID string) error {
	if !uuidPattern.MatchString(response.ApprovalID) {
		return fmt.Errorf("approval status has an invalid approval_id")
	}
	if response.RequestID != requestID || !requestIDPattern.MatchString(response.RequestID) {
		return fmt.Errorf("approval status has an invalid request_id")
	}
	if _, err := time.Parse(time.RFC3339, response.ExpiresAt); err != nil {
		return fmt.Errorf("approval status has an invalid expires_at: %w", err)
	}
	if response.ResolvedAt != nil {
		if _, err := time.Parse(time.RFC3339, *response.ResolvedAt); err != nil {
			return fmt.Errorf("approval status has an invalid resolved_at: %w", err)
		}
	}
	switch response.Status {
	case ApprovalPending:
		if response.ResolvedAt != nil {
			return fmt.Errorf("pending approval status cannot be resolved")
		}
	case ApprovalApproved, ApprovalRejected, ApprovalExpired, ApprovalConsumed, ApprovalCancelled:
	default:
		return fmt.Errorf("approval status has an unsupported status %q", response.Status)
	}
	return nil
}

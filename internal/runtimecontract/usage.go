package runtimecontract

import (
	"context"
	"fmt"
	"net/http"
)

// LLM usage metering (ENG-261).
//
// The proxy observes the agent's LLM API traffic on metered origins and
// reports usage counters per request — model id, token counts, stop reason,
// latency. Never prompt or completion content: the meter-only guarantee is
// enforced at the proxy, and this wire shape has no field that could carry it.
//
// Mirrors packages/contracts/src/runtime/usage.ts in the platform monorepo
// (session-usage-report.schema.json). The server dedupes on
// [session, request_id], so retried batches are no-ops.

// SessionUsagePath is the KIT-authenticated ingest endpoint.
const SessionUsagePath = "/v1/runtime/sessions/usage"

// The usage additions are independently pinned; older runtime contracts retain
// their existing bundle version and checksums.
const UsageContractBundleVersion = "1.4.0"

// MaxUsageEventsPerReport caps one report batch (contract limit).
const MaxUsageEventsPerReport = 100

// SessionUsageEvent is one observed LLM API request.
type SessionUsageEvent struct {
	RequestID   string `json:"request_id"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	ServiceTier string `json:"service_tier,omitempty"`
	// InputTokens excludes cache reads/writes (Anthropic semantics; for OpenAI
	// the cached share of prompt_tokens is moved to CacheReadTokens).
	InputTokens int `json:"input_tokens"`
	// OutputTokens is nil when the provider's response omitted usage (e.g. an
	// OpenAI stream without stream_options.include_usage) — recorded and
	// flagged server-side, never estimated by the client.
	OutputTokens        *int   `json:"output_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	StopReason          string `json:"stop_reason,omitempty"`
	LatencyMS           int    `json:"latency_ms,omitempty"`
	OccurredAt          string `json:"occurred_at"`
}

type SessionUsageReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Events        []SessionUsageEvent `json:"events"`
}

type SessionUsageReportResponse struct {
	SchemaVersion int `json:"schema_version"`
	Accepted      int `json:"accepted"`
	Duplicates    int `json:"duplicates"`
}

// ReportSessionUsage posts one batch of usage events under the session's KIT.
func ReportSessionUsage(
	ctx context.Context,
	client *http.Client,
	baseURL, runtimeToken string,
	events []SessionUsageEvent,
) (*SessionUsageReportResponse, error) {
	if runtimeToken == "" {
		return nil, fmt.Errorf("usage reporting requires a session token")
	}
	if len(events) == 0 || len(events) > MaxUsageEventsPerReport {
		return nil, fmt.Errorf(
			"usage report must carry 1-%d events", MaxUsageEventsPerReport,
		)
	}
	raw, err := executeRuntimeJSON(
		ctx,
		client,
		baseURL,
		runtimeToken,
		SessionUsagePath,
		SessionUsageReport{SchemaVersion: SchemaVersion, Events: events},
		"usage report",
	)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var result SessionUsageReportResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, err
	}
	if result.SchemaVersion != SchemaVersion ||
		result.Accepted < 0 || result.Duplicates < 0 {
		return nil, fmt.Errorf("usage report response is invalid")
	}
	return &result, nil
}

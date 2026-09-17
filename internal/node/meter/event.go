package meter

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// BuildEvent assembles the wire event for one observed inference request.
// A response whose usage was never observed ships with zero input and a nil
// output count — the server books it unpriced (tokens counted where known,
// cost excluded, never guessed).
func BuildEvent(
	provider string,
	info RequestInfo,
	totals UsageTotals,
	started time.Time,
) runtimecontract.SessionUsageEvent {
	model := totals.Model
	if model == "" {
		model = info.Model
	}
	tier := "default"
	switch provider {
	case "openai":
		tier = totals.ServiceTier
		switch tier {
		case "fast":
			tier = "priority"
		case "default", "flex", "priority", "scale":
		default:
			tier = "unknown"
		}
	case "anthropic":
		// Fast mode has published rates and is catalogued under priority, like
		// OpenAI's fast tier. An Anthropic Priority Tier commitment is contract
		// priced, so it is reported unknown and stays unpriced until an admin
		// publishes a rate. Standard and unreported tiers are default.
		switch {
		case totals.Speed == "fast":
			tier = "priority"
		case totals.ServiceTier == "priority":
			tier = "unknown"
		}
	}
	if !totals.HasInput || totals.Invalid {
		totals.OutputTokens = nil
	}
	event := runtimecontract.SessionUsageEvent{
		RequestID:           NewRequestID(),
		Provider:            provider,
		Model:               model,
		ServiceTier:         tier,
		UsageSource:         info.Source,
		UsageTransport:      info.Transport,
		InputTokens:         totals.InputTokens,
		OutputTokens:        totals.OutputTokens,
		CacheCreationTokens: totals.CacheCreationTokens,
		CacheReadTokens:     totals.CacheReadTokens,
		StopReason:          totals.StopReason,
		LatencyMS:           int(min(2147483647, max(0, time.Since(started).Milliseconds()))),
		OccurredAt:          started.UTC().Format(time.RFC3339Nano),
	}
	return event
}

// Response identity is stable across reconnects and HTTP/WebSocket fallback.
// The backend scopes deduplication to the authenticated session owner/handle.
func responseRequestID(provider, source, responseID string) string {
	return fmt.Sprintf("llm-%x", sha256.Sum256([]byte(provider+"\x00"+source+"\x00"+responseID)))
}

package meter

import (
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
	if provider == "openai" {
		tier = totals.ServiceTier
		switch tier {
		case "fast":
			tier = "priority"
		case "default", "flex", "priority", "scale":
		default:
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

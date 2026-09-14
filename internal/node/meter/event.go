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
	event := runtimecontract.SessionUsageEvent{
		RequestID:           NewRequestID(),
		Provider:            provider,
		Model:               info.Model,
		InputTokens:         totals.InputTokens,
		OutputTokens:        totals.OutputTokens,
		CacheCreationTokens: totals.CacheCreationTokens,
		CacheReadTokens:     totals.CacheReadTokens,
		StopReason:          totals.StopReason,
		LatencyMS:           int(time.Since(started).Milliseconds()),
		OccurredAt:          started.UTC().Format(time.RFC3339Nano),
	}
	return event
}

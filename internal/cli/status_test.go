package cli

import (
	"strings"
	"testing"
)

// TestStatusReportsCostMetering pins the user-visible state of the metering
// switch: enabled reports the counter guarantee, disabled names the opt-out.
func TestStatusReportsCostMetering(t *testing.T) {
	cfg := uxConfig(t)
	for _, tc := range []struct {
		enabled bool
		state   string
	}{
		{true, "ok"},
		{false, "inactive"},
	} {
		cfg.CostMetering = tc.enabled
		report := collectStatus(cfg, "codex", true, false)
		var found bool
		for _, check := range report.Checks {
			if check.Name != "Cost metering" {
				continue
			}
			found = true
			if check.State != tc.state {
				t.Errorf("Cost metering state with enabled=%v = %q, want %q", tc.enabled, check.State, tc.state)
			}
			if tc.enabled && !strings.Contains(check.Detail, "never prompt content") {
				t.Errorf("enabled Cost metering detail = %q, want the content guarantee", check.Detail)
			}
		}
		if !found {
			t.Fatalf("Cost metering check missing with enabled=%v", tc.enabled)
		}
	}
}

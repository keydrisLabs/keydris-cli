package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestResolveHookSessionIDKeepsClaudeAndCodexNamespacesSeparate(t *testing.T) {
	t.Setenv("KEYDRIS_SESSION", "run-wrapper")
	t.Setenv(sessionOwnerEnv, "")

	if got := resolveHookSessionID("codex-thread", hookHarnessCodex); got != "run-wrapper" {
		t.Fatalf("Codex session = %q, want wrapper session", got)
	}
	if got := resolveHookSessionID("claude-native", hookHarnessClaude); got != "claude-native" {
		t.Fatalf("Claude session = %q, want payload session", got)
	}

	t.Setenv(sessionOwnerEnv, sessionOwnerRun)
	if got := resolveHookSessionID("claude-native", hookHarnessClaude); got != "run-wrapper" {
		t.Fatalf("wrapped Claude session = %q, want wrapper session", got)
	}
}

func TestCodexRequiresWrapperSession(t *testing.T) {
	t.Setenv("KEYDRIS_SESSION", "")
	if got := resolveHookSessionID("codex-thread", hookHarnessCodex); got != "" {
		t.Fatalf("standalone Codex unexpectedly used its thread id: %q", got)
	}
}

func TestPermissionRequestAllowUsesCodexHookSchema(t *testing.T) {
	var output bytes.Buffer
	emitPermissionRequestAllow(&output)

	var decoded struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior string `json:"behavior"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.HookSpecificOutput.HookEventName != "PermissionRequest" ||
		decoded.HookSpecificOutput.Decision.Behavior != "allow" {
		t.Fatalf("unexpected permission output: %s", output.String())
	}
}

func TestClaudeApprovalDecisionRemainsAsk(t *testing.T) {
	verdict, reason := commandVerdict(
		runtimecontract.DecisionApprovalRequired,
		"keydris_approval_required",
	)
	if verdict != "ask" || !strings.Contains(reason, "requires approval") {
		t.Fatalf("approval verdict = %q, reason = %q", verdict, reason)
	}

	var output bytes.Buffer
	writePreToolVerdict(&output, verdict, reason)
	var decoded struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.HookSpecificOutput.HookEventName != "PreToolUse" ||
		decoded.HookSpecificOutput.Decision != "ask" {
		t.Fatalf("Claude hook output changed: %s", output.String())
	}
}

func TestPreToolPayloadWithoutCommandStillSkips(t *testing.T) {
	verdict, _ := decidePreToolUse(
		strings.NewReader(`{"session_id":"claude","tool_name":"Read","tool_input":{}}`),
		hookHarnessClaude,
	)
	if verdict != "skip" {
		t.Fatalf("verdict = %q, want skip", verdict)
	}
}

func TestPreToolPayloadRejectsDuplicateKeysAndOversizeInput(t *testing.T) {
	duplicate := `{"session_id":"claude","tool_input":{"command":"safe","command":"unsafe"}}`
	if verdict, _ := decidePreToolUse(strings.NewReader(duplicate), hookHarnessClaude); verdict != "deny" {
		t.Fatalf("duplicate-key verdict = %q, want deny", verdict)
	}
	oversize := strings.Repeat(" ", maxHookInputBytes+1)
	if verdict, _ := decidePreToolUse(strings.NewReader(oversize), hookHarnessClaude); verdict != "deny" {
		t.Fatalf("oversize verdict = %q, want deny", verdict)
	}
}

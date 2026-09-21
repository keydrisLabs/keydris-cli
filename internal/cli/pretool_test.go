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
	writeCodexPermissionVerdict(&output, "allow", "allowed by policy")

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
		"npm publish",
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

func TestCommandVerdictFormatsPolicyDenialAsBox(t *testing.T) {
	verdict, reason := commandVerdict(
		runtimecontract.DecisionDeny,
		"keydris_policy_denied",
		"git status",
	)
	if verdict != "deny" {
		t.Fatalf("verdict = %q, want deny", verdict)
	}
	for _, want := range []string{"╔", "╚", "COMMAND DENIED", "git status", "keydris_policy_denied"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q missing %q", reason, want)
		}
	}
}

func TestCommandVerdictKeepsPlainMessageForInfraDenials(t *testing.T) {
	verdict, reason := commandVerdict(
		runtimecontract.DecisionDeny,
		"keydris_policy_unavailable",
		"git status",
	)
	if verdict != "deny" {
		t.Fatalf("verdict = %q, want deny", verdict)
	}
	if strings.Contains(reason, "╔") {
		t.Fatalf("infra denial unexpectedly boxed: %q", reason)
	}
	if reason != "keydris: denied by policy (keydris_policy_unavailable)" {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestPreToolPayloadWithoutCommandDenies(t *testing.T) {
	for _, harness := range []hookHarness{hookHarnessClaude, hookHarnessCodex} {
		for _, input := range []string{`{}`, `{"tool_name":"Bash","tool_input":{}}`, `{"tool_input":{"command":"  \t"}}`} {
			verdict, reason := decidePreToolUse(strings.NewReader(input), harness)
			if verdict != "deny" || reason == "" {
				t.Fatalf("harness %d: malformed input %s returned %q, %q", harness, input, verdict, reason)
			}
		}
	}
}

func TestCodexBlocksEveryNonAllowAtBothHookEvents(t *testing.T) {
	for _, verdict := range []string{"deny", "ask", "", "unexpected"} {
		t.Run(verdict, func(t *testing.T) {
			for _, event := range []struct {
				name  string
				write func(*bytes.Buffer)
			}{
				{"PreToolUse", func(out *bytes.Buffer) { writeCodexPreToolVerdict(out, verdict, "") }},
				{"PermissionRequest", func(out *bytes.Buffer) { writeCodexPermissionVerdict(out, verdict, "") }},
			} {
				var output bytes.Buffer
				event.write(&output)
				if !isCodexProbeDenial(output.Bytes(), event.name) {
					t.Fatalf("%s failed to deny %q: %s", event.name, verdict, output.String())
				}
				if verdict == "ask" && !strings.Contains(output.String(), "requires policy approval") {
					t.Fatalf("approval denial lacks an explanation: %s", output.String())
				}
			}
		})
	}
	var output bytes.Buffer
	writeCodexPreToolVerdict(&output, "allow", "allowed")
	if output.Len() != 0 {
		t.Fatalf("allowed command was blocked: %s", output.String())
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

// TestCodexHookWritersPreserveDenialReasons pins the fail-closed messaging: a
// policy denial must reach Codex with the policy-authored box intact, and an
// allowed command must not carry rejection text into the PermissionRequest
// decision.
func TestCodexHookWritersPreserveDenialReasons(t *testing.T) {
	verdict, reason := commandVerdict(runtimecontract.DecisionDeny, denialBoxReasonCode, "terraform destroy")
	if verdict != "deny" || !strings.Contains(reason, "COMMAND DENIED") {
		t.Fatalf("policy denial fixture = %q, %q", verdict, reason)
	}

	var preTool bytes.Buffer
	writeCodexPreToolVerdict(&preTool, verdict, reason)
	var preToolOut struct {
		HookSpecificOutput struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(preTool.Bytes(), &preToolOut); err != nil {
		t.Fatal(err)
	}
	if preToolOut.HookSpecificOutput.Event != "PreToolUse" ||
		preToolOut.HookSpecificOutput.Decision != "deny" ||
		preToolOut.HookSpecificOutput.Reason != reason {
		t.Fatalf("PreToolUse denial lost its reason: %s", preTool.String())
	}

	type permissionOutput struct {
		HookSpecificOutput struct {
			Event    string            `json:"hookEventName"`
			Decision map[string]string `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	decodePermission := func(raw []byte) permissionOutput {
		t.Helper()
		var decoded permissionOutput
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	var permission bytes.Buffer
	writeCodexPermissionVerdict(&permission, verdict, reason)
	denied := decodePermission(permission.Bytes())
	if denied.HookSpecificOutput.Event != "PermissionRequest" ||
		denied.HookSpecificOutput.Decision["behavior"] != "deny" ||
		denied.HookSpecificOutput.Decision["message"] != reason {
		t.Fatalf("PermissionRequest denial lost its reason: %s", permission.String())
	}

	var allowed bytes.Buffer
	writeCodexPermissionVerdict(&allowed, "allow", "keydris: allowed by policy")
	passthrough := decodePermission(allowed.Bytes())
	if passthrough.HookSpecificOutput.Decision["behavior"] != "allow" {
		t.Fatalf("allowed command was not passed through: %s", allowed.String())
	}
	if _, present := passthrough.HookSpecificOutput.Decision["message"]; present {
		t.Fatalf("allowed command carries a rejection message: %s", allowed.String())
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestUnresolvedApprovalDecisionFailsClosed(t *testing.T) {
	verdict, reason := commandVerdict(
		runtimecontract.DecisionApprovalRequired,
		"keydris_approval_required",
		"npm publish",
	)
	if verdict != "deny" || !strings.Contains(reason, "not resolved") {
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
		decoded.HookSpecificOutput.Decision != "deny" {
		t.Fatalf("unresolved approval did not fail closed: %s", output.String())
	}
}

func TestCommandApprovalRetriesIdenticalRequestAfterConsoleApproval(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case commandsAuthorizeP:
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, body)
			requestID := body["request_id"].(string)
			decision, reason := "approval_required", "keydris_approval_required"
			if len(requests) == 2 {
				decision, reason = "allow", "keydris_approval_granted"
			}
			fmt.Fprintf(writer, `{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":%q,"attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","obligations":[],"decision":%q,"reason_code":%q}`, requestID, decision, reason)
		case runtimecontract.ApprovalStatusEndpointPath:
			if len(requests) != 1 || request.URL.Query().Get("request_id") != requests[0]["request_id"] {
				t.Fatalf("approval status query did not use original request id")
			}
			fmt.Fprintf(writer, `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":%q,"status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":"2029-12-31T23:59:00Z"}`, requests[0]["request_id"])
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	input := preToolInput{ToolName: "Bash", CWD: "/workspace"}
	input.ToolInput.Command = "npm publish"
	decision, reason, err := authorizeCommandWithClient(
		server.Client(), server.URL, "kit-token", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != runtimecontract.DecisionAllow || reason != "keydris_approval_granted" {
		t.Fatalf("decision=%q reason=%q", decision, reason)
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("authorization retry changed request: %+v", requests)
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

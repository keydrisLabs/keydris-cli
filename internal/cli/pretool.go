package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// runPreToolUse implements `keydris __pretool-use`, the PreToolUse hook wired
// by `keydris init`: it relays the shell command the coding tool wants to run
// to POST /v1/runtime/commands/authorize and maps the decision to the
// harness's permission verdict.
//
// Fail-closed by construction: both Claude Code and Codex fail OPEN when a
// hook crashes, times out, or prints invalid JSON — so every error path here
// emits an explicit deny verdict and exits 0. Keep the configured hook timeout
// well above preToolUseTimeout.
//
// approval_required is resolved inside this hook: it waits for the user to act
// in the Keydris console and then retries the identical authorization request.
// This keeps both Claude Code and Codex paused without delegating the decision
// to a harness-specific terminal prompt.

const (
	preToolUseTimeout   = 5 * time.Second
	approvalWaitTimeout = 10 * time.Minute
	maxHookInputBytes   = 10 << 20 // Claude Code tool inputs can be large
	commandsAuthorizeP  = "/v1/runtime/commands/authorize"
)

type hookHarness int

const (
	hookHarnessClaude hookHarness = iota
	hookHarnessCodex
)

// preToolInput is the harness-supplied hook payload (Claude Code shape; the
// Codex wrapper exports KEYDRIS_SESSION so session resolution works there too).
type preToolInput struct {
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	CWD       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func runPreToolUse(args []string) int {
	codex := len(args) > 0 && args[0] == "--codex"
	harness := hookHarnessClaude
	if codex {
		harness = hookHarnessCodex
	}
	verdict, reason := decidePreToolUse(os.Stdin, harness)
	if codex {
		if verdict == "skip" {
			return 0
		}
		emitPreToolVerdict(verdict, reason)
		return 0
	}
	if verdict == "skip" {
		// The Claude matcher restricts this hook to shell tools, so a payload
		// without a command is malformed input, not another tool: refuse.
		verdict, reason = "deny", "keydris: hook payload carries no command"
	}
	emitPreToolVerdict(verdict, reason)
	return 0
}

// runPermissionRequest implements `keydris __permission-request`, the Codex
// hook that resolves policy-allowed commands without an interactive prompt.
// Approval-required commands are also paused here when Codex invokes this hook
// independently of PreToolUse.
func runPermissionRequest(args []string) int {
	verdict, _ := decidePreToolUse(os.Stdin, hookHarnessCodex)
	if verdict == "allow" {
		emitPermissionRequestAllow(os.Stdout)
	}
	return 0
}

// decidePreToolUse resolves the session and asks the control plane. It only
// ever returns ("allow"|"deny"|"skip", reason); "skip" means the payload
// carries no shell command, so there is nothing to gate.
func decidePreToolUse(stdin io.Reader, harness hookHarness) (string, string) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxHookInputBytes+1))
	if err != nil {
		return "deny", "keydris: could not read the hook payload"
	}
	if len(raw) > maxHookInputBytes {
		return "deny", "keydris: hook payload exceeds the size limit"
	}
	if err := runtimecontract.RejectDuplicateJSONKeys(raw); err != nil {
		return "deny", "keydris: invalid hook payload"
	}
	var input preToolInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return "deny", "keydris: invalid hook payload"
	}
	if input.ToolInput.Command == "" {
		return "skip", ""
	}

	sid := resolveHookSessionID(input.SessionID, harness)
	if sid == "" {
		return "deny", "keydris: no session in the hook payload"
	}
	cfg := config.Load()
	if err := validateSessionID(sid); err != nil {
		return "deny", "keydris: invalid session id"
	}
	state, err := loadState(cfg, sid)
	if err != nil || state.KIT == "" {
		return "deny", "keydris: no active Keydris session (run inside `keydris`-configured tooling)"
	}

	kit := state.KIT
	if current, lookupErr := lookupRegisteredSession(cfg, state.Handle); lookupErr == nil {
		if current.SessionID != sid {
			return "deny", "keydris: daemon session does not match the hook session"
		}
		kit = current.SVID
	}
	decision, reason, err := authorizeCommand(cfg, kit, input)
	if err != nil {
		return "deny", "keydris: command authorization unavailable: " + err.Error()
	}
	return commandVerdict(decision, reason, input.ToolInput.Command)
}

func commandVerdict(decision runtimecontract.NormalizedDecision, reason, command string) (string, string) {
	switch decision {
	case runtimecontract.DecisionAllow:
		return "allow", "keydris: allowed by policy"
	case runtimecontract.DecisionApprovalRequired:
		return "deny", "keydris: approval was not resolved"
	default:
		if reason == "" {
			reason = string(decision)
		}
		if reason == denialBoxReasonCode {
			return "deny", formatPolicyDenialBox(command, reason)
		}
		return "deny", "keydris: denied by policy (" + reason + ")"
	}
}

// Codex always supplies its own thread session_id, which is unrelated to the
// Keydris wrapper session. The wrapper's environment is therefore authoritative
// for Codex. Claude keeps its native payload ID, except when it is itself
// running inside `keydris run` and must reuse the wrapper-owned session.
func resolveHookSessionID(payloadSessionID string, harness hookHarness) string {
	wrapperSessionID := os.Getenv("KEYDRIS_SESSION")
	if harness == hookHarnessCodex || os.Getenv(sessionOwnerEnv) == sessionOwnerRun {
		return wrapperSessionID
	}
	if payloadSessionID != "" {
		return payloadSessionID
	}
	return wrapperSessionID
}

func authorizeCommand(
	cfg *config.Config,
	kit string,
	input preToolInput,
) (decision runtimecontract.NormalizedDecision, reasonCode string, err error) {
	client, err := mTLSClient(cfg)
	if err != nil {
		return "", "", err
	}
	return authorizeCommandWithClient(client, cfg.ControlMTLSURL, kit, input)
}

func authorizeCommandWithClient(
	client *http.Client,
	baseURL, kit string,
	input preToolInput,
) (decision runtimecontract.NormalizedDecision, reasonCode string, err error) {
	requestID := "cli-" + newProxyToken()
	body, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"request_id":     requestID,
		"command":        input.ToolInput.Command,
		"cwd":            input.CWD,
		"tool_name":      input.ToolName,
	})
	if err != nil {
		return "", "", err
	}
	decision, reasonCode, err = authorizeCommandOnce(
		client, baseURL, kit, requestID, body,
	)
	if err != nil || decision != runtimecontract.DecisionApprovalRequired {
		return decision, reasonCode, err
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), approvalWaitTimeout)
	defer waitCancel()
	if err := runtimecontract.WaitForApproval(
		waitCtx, client, baseURL, kit, requestID,
	); err != nil {
		return "", "", err
	}
	return authorizeCommandOnce(client, baseURL, kit, requestID, body)
}

func authorizeCommandOnce(
	client *http.Client,
	baseURL, kit, requestID string,
	body []byte,
) (decision runtimecontract.NormalizedDecision, reasonCode string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), preToolUseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+commandsAuthorizeP,
		bytes.NewReader(body),
	)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+kit)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("authorize endpoint %s", resp.Status)
	}
	out, err := runtimecontract.DecodeDecisionResponse(resp.Body)
	if err != nil {
		return "", "", err
	}
	if out.RequestID != requestID {
		return "", "", fmt.Errorf("authorize response request id does not match")
	}
	return out.Decision, out.ReasonCode, nil
}

func emitPermissionRequestAllow(writer io.Writer) {
	fmt.Fprintln(writer, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`)
}

// emitPreToolVerdict prints the Claude Code PreToolUse hook response. Codex's
// PreToolUse accepts the allow/deny permissionDecision vocabulary used here.
func emitPreToolVerdict(verdict, reason string) {
	writePreToolVerdict(os.Stdout, verdict, reason)
}

func writePreToolVerdict(writer io.Writer, verdict, reason string) {
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       verdict,
			"permissionDecisionReason": reason,
		},
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		// Last-ditch deny: never exit without a verdict.
		fmt.Fprintln(writer, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"keydris: verdict encoding failed"}}`)
		return
	}
	fmt.Fprintln(writer, string(encoded))
}

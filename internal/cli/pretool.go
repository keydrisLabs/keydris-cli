package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
		// Codex treats a zero exit with no output as success. A bare
		// permissionDecision:"allow" is unsupported unless updatedInput is
		// also supplied, so only emit a structured response for denials.
		writeHarnessPreToolVerdict(os.Stdout, harness, verdict, reason)
		return 0
	}
	writeHarnessPreToolVerdict(os.Stdout, harness, verdict, reason)
	return 0
}

func writeHarnessPreToolVerdict(writer io.Writer, harness hookHarness, verdict, reason string) {
	if harness == hookHarnessCodex {
		writeCodexPreToolVerdict(writer, verdict, reason)
		return
	}
	writePreToolVerdict(writer, verdict, reason)
}

// runPermissionRequest implements `keydris __permission-request`, the Codex
// hook that resolves policy-allowed commands without an interactive prompt.
// Approval-required commands are also paused here when Codex invokes this hook
// independently of PreToolUse. A policy denial must not become an overridable
// native approval prompt.
func runPermissionRequest(args []string) int {
	verdict, reason := decidePreToolUse(os.Stdin, hookHarnessCodex)
	writeCodexPermissionVerdict(os.Stdout, verdict, reason)
	return 0
}

// decidePreToolUse resolves the session and asks the control plane. It only
// ever returns ("allow"|"deny", reason). Both integrations register this hook
// only for shell commands, so missing commands are malformed input.
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
	if strings.TrimSpace(input.ToolInput.Command) == "" {
		return "deny", "keydris: hook payload carries no command"
	}

	sid := resolveHookSessionID(input.SessionID, harness)
	if sid == "" {
		return "deny", "keydris: no session in the hook payload"
	}
	cfg := config.Load()
	if cfg.ManagedConfigError != nil {
		return "deny", "keydris: managed configuration is invalid"
	}
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
	decision, reason, err := authorizeCommand(cfg, kit, input, os.Stderr)
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
// running inside `keydris run` and must reuse the wrapper-owned session, or
// inside `keydris claude-desktop`, whose launch session covers every Code tab.
func resolveHookSessionID(payloadSessionID string, harness hookHarness) string {
	wrapperSessionID := os.Getenv("KEYDRIS_SESSION")
	if harness == hookHarnessCodex || os.Getenv(sessionOwnerEnv) == sessionOwnerRun {
		return wrapperSessionID
	}
	if desktopSID := desktopSessionID(); desktopSID != "" {
		return desktopSID
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
	approvalWriter io.Writer,
) (decision runtimecontract.NormalizedDecision, reasonCode string, err error) {
	client, err := mTLSClient(cfg)
	if err != nil {
		return "", "", err
	}
	return authorizeCommandWithClient(client, cfg.ControlMTLSURL, kit, input, approvalWriter)
}

func authorizeCommandWithClient(
	client *http.Client,
	baseURL, kit string,
	input preToolInput,
	approvalWriter io.Writer,
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
	if approvalWriter != nil {
		fmt.Fprintln(approvalWriter, "keydris: approval required. Open the Keydris console to allow or reject this action; waiting up to 10 minutes.")
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), approvalWaitTimeout)
	defer waitCancel()
	if err := runtimecontract.WaitForApproval(
		waitCtx, client, baseURL, kit, requestID,
	); err != nil {
		if approvalWriter != nil {
			fmt.Fprintf(approvalWriter, "keydris: approval was not granted: %v\n", err)
		}
		return "", "", err
	}
	if approvalWriter != nil {
		fmt.Fprintln(approvalWriter, "keydris: approval granted; continuing.")
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

func codexCommandVerdict(verdict, reason string) (string, string) {
	if verdict == "allow" {
		return verdict, reason
	}
	if verdict == "ask" {
		reason = "keydris: this command requires policy approval; Codex cannot reliably request that approval, so execution is blocked"
	}
	if reason == "" {
		reason = "keydris: command authorization did not return an allow decision"
	}
	return "deny", reason
}

func writeCodexPreToolVerdict(writer io.Writer, verdict, reason string) {
	verdict, reason = codexCommandVerdict(verdict, reason)
	if verdict == "deny" {
		writePreToolVerdict(writer, verdict, reason)
	}
}

func writeCodexPermissionVerdict(writer io.Writer, verdict, reason string) {
	verdict, reason = codexCommandVerdict(verdict, reason)
	decision := map[string]string{"behavior": verdict}
	if verdict == "deny" {
		decision["message"] = reason
	}
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      decision,
		},
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		fmt.Fprintln(writer, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"keydris: verdict encoding failed"}}}`)
		return
	}
	fmt.Fprintln(writer, string(encoded))
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

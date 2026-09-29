package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const codexProbeInput = `{"tool_name":"Bash","tool_input":{"command":"keydris-hook-probe"}}`

type codexHookRunner func(context.Context, string) ([]byte, error)

// Verify the shell-to-hook boundary before minting a session. This probe has
// no session identity, so it must produce an explicit denial without contacting
// the control plane. Configuration equality alone cannot detect a broken shell
// invocation, and Codex can continue when a hook exits unsuccessfully.
func verifyCodexHookExecution(opt sandbox.CodexHookOptions) error {
	return verifyCodexHookRunner(opt, runCodexHookProbe)
}

func verifyCodexHookRunner(opt sandbox.CodexHookOptions, run codexHookRunner) error {
	for _, hook := range []struct{ event, command string }{
		{"PreToolUse", opt.PreToolUseHook},
		{"PermissionRequest", opt.PermissionRequestHook},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := run(ctx, hook.command)
		cancel()
		if err != nil {
			return fmt.Errorf("%s hook could not execute: %w", hook.event, err)
		}
		if !isCodexProbeDenial(output, hook.event) {
			return fmt.Errorf("%s hook did not return an explicit denial for the sessionless probe", hook.event)
		}
	}
	return nil
}

func isCodexProbeDenial(output []byte, event string) bool {
	if runtimecontract.RejectDuplicateJSONKeys(output) != nil {
		return false
	}
	var result struct {
		HookSpecificOutput struct {
			Event      string `json:"hookEventName"`
			Permission string `json:"permissionDecision"`
			Decision   struct {
				Behavior string `json:"behavior"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(output, &result) != nil || result.HookSpecificOutput.Event != event {
		return false
	}
	if event == "PreToolUse" {
		return result.HookSpecificOutput.Permission == "deny"
	}
	return event == "PermissionRequest" && result.HookSpecificOutput.Decision.Behavior == "deny"
}

func runCodexHookProbe(ctx context.Context, command string) ([]byte, error) {
	cmd := codexProbeShell(ctx, command)
	// Strip case variants too: Windows environment names are case-insensitive.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "KEYDRIS_SESSION") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stdin = strings.NewReader(codexProbeInput)
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd.Output()
}

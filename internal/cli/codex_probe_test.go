package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// Re-exec the test binary as the real CLI to exercise the generated shell
// command, stdin decoding, dispatch, and verdict serialization together.
func TestMain(m *testing.M) {
	if os.Getenv("KEYDRIS_TEST_CODEX_HOOK_PROCESS") == "1" {
		if os.Getenv("KEYDRIS_SESSION") != "" {
			os.Exit(99) // The startup probe must not inherit an active session.
		}
		if os.Getenv("KEYDRIS_TEST_CODEX_HOOK_SILENT") == "1" {
			os.Exit(0) // A hook that runs but issues no verdict.
		}
		os.Exit(Execute())
	}
	if os.Getenv("KEYDRIS_TEST_CODEX_ENV_DUMP") == "1" {
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if strings.EqualFold(key, "KEYDRIS_SESSION") || strings.EqualFold(key, "KEYDRIS_SESSION_ID") {
				fmt.Println(entry)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexHooksExecuteFromPathWithSpaces(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "Keydris Agent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "keydris"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	src, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy executable: %v, %v", copyErr, closeErr)
	}
	quoted := shellQuote(path)
	if runtime.GOOS == "windows" {
		quoted = `"` + filepath.ToSlash(path) + `"`
	}
	opt := codexHooks(quoted)
	t.Setenv("KEYDRIS_TEST_CODEX_HOOK_PROCESS", "1")
	t.Setenv("KEYDRIS_SESSION", "must-not-be-used-by-probe")
	if err := verifyCodexHookExecution(opt); err != nil {
		t.Fatalf("generated hooks did not execute and deny: %v", err)
	}
	if runtime.GOOS == "windows" {
		legacy := opt
		legacy.PreToolUseHook = strings.TrimPrefix(opt.PreToolUseHook, "& ")
		if err := verifyCodexHookExecution(legacy); err == nil {
			t.Fatal("legacy PowerShell string expression passed the execution probe")
		}
	}
	// Configuration migration must recognize the invocation it just generated.
	settings := filepath.Join(dir, "hooks.json")
	if err := sandbox.ConfigureCodexHooks(settings, opt); err != nil {
		t.Fatal(err)
	}
	if wired, err := sandbox.VerifyCodexHooks(settings, opt); err != nil || !wired {
		t.Fatalf("generated configuration failed verification: %v, %v", wired, err)
	}
	if removed, err := sandbox.DeconfigureCodexHooks(settings); err != nil || !removed {
		t.Fatalf("generated hook could not be removed: %v, %v", removed, err)
	}
}

func TestCodexProbeRejectsFailedOrNonDenyHooks(t *testing.T) {
	opt := sandbox.CodexHookOptions{PreToolUseHook: "pre", PermissionRequestHook: "permission"}
	for _, event := range []string{"pre", "permission"} {
		allow := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`
		duplicate := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecision":"deny"}}`
		trailing := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}} {}`
		if event == "permission" {
			allow = `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`
			duplicate = `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","behavior":"deny"}}}`
			trailing = `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny"}}} {}`
		}
		for _, tc := range []struct {
			name   string
			output string
			err    error
		}{
			{"process failure", "", errors.New("exit status 1")},
			{"timeout", "", context.DeadlineExceeded},
			{"silent", "", nil},
			{"invalid JSON", "not JSON", nil},
			{"allow", allow, nil},
			{"wrong event", `{"hookSpecificOutput":{"hookEventName":"Other","permissionDecision":"deny"}}`, nil},
			{"duplicate key", duplicate, nil},
			{"trailing output", trailing, nil},
		} {
			t.Run(event+"/"+tc.name, func(t *testing.T) {
				err := verifyCodexHookRunner(opt, func(ctx context.Context, command string) ([]byte, error) {
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("probe has no deadline")
					}
					if command == event {
						return []byte(tc.output), tc.err
					}
					var out bytes.Buffer
					writeCodexPreToolVerdict(&out, "deny", "missing session")
					return out.Bytes(), nil
				})
				if err == nil {
					t.Fatal("unsafe hook passed startup verification")
				}
			})
		}
	}
}

func TestCodexProbeCancellation(t *testing.T) {
	opt, err := codexHookOptions()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runCodexHookProbe(ctx, opt.PreToolUseHook); err == nil {
		t.Fatal("cancelled probe started successfully")
	}
}

// TestCodexHookProbeStripsSessionAliases pins the case-insensitive credential
// strip the startup probe depends on: Windows environment names are
// case-insensitive, so a session credential under any spelling would let the
// probe run as an authenticated session. Unrelated variables must survive.
func TestCodexHookProbeStripsSessionAliases(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quoted := shellQuote(executable)
	if runtime.GOOS == "windows" {
		quoted = "\"" + filepath.ToSlash(executable) + "\""
	}
	command := codexHooks(quoted).PreToolUseHook
	t.Setenv("KEYDRIS_TEST_CODEX_ENV_DUMP", "1")
	t.Setenv("KEYDRIS_SESSION", "exact")
	t.Setenv("Keydris_Session", "mixed")
	t.Setenv("keydris_session", "lower")
	t.Setenv("KEYDRIS_SESSION_ID", "kept")

	output, err := runCodexHookProbe(context.Background(), command)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if strings.EqualFold(key, "KEYDRIS_SESSION") {
			t.Fatalf("probe inherited the session credential %q", value)
		}
	}
	if !strings.Contains(string(output), "KEYDRIS_SESSION_ID=kept") {
		t.Fatalf("probe dropped unrelated environment variables: %q", output)
	}
}

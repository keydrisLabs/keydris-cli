package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// writeEarlierCodexHooks writes hooks.json as an earlier release left it: a
// user hook beside the Keydris entries.
func writeEarlierCodexHooks(t *testing.T, cfg *config.Config) {
	t.Helper()
	raw := `{"hooks":{
		"PreToolUse":[
			{"hooks":[{"type":"command","command":"my-audit-hook"}]},
			{"matcher":"^Bash$","hooks":[{"type":"command","command":"'/usr/local/bin/keydris' __pretool-use --codex","timeout":30}]}],
		"PermissionRequest":[{"matcher":"^Bash$","hooks":[{"type":"command","command":"'/usr/local/bin/keydris' __permission-request","timeout":30}]}],
		"SessionStart":[{"hooks":[{"type":"command","command":"'/usr/local/bin/keydris' __agent-context","timeout":30}]}]}}`
	if err := os.MkdirAll(filepath.Dir(cfg.CodexHooksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CodexHooksPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInitCodexMovesOffHooksJSON(t *testing.T) {
	cfg := uxConfig(t)
	writeEarlierCodexHooks(t, cfg)
	// Earlier installs still count as configured until init migrates them.
	if configured, err := integrationConfigured(cfg, "codex"); err != nil || !configured {
		t.Fatalf("earlier install not recognized: %v, %v", configured, err)
	}
	removed, err := configureCodex(cfg)
	if err != nil || !removed || !codexConfigured(cfg) {
		t.Fatalf("configure = %v, %v; recorded = %v", removed, err, codexConfigured(cfg))
	}
	if legacy, err := legacyCodexHooks(cfg); err != nil || legacy {
		t.Fatalf("Keydris entries remain in hooks.json: %v, %v", legacy, err)
	}
	raw, err := os.ReadFile(cfg.CodexHooksPath)
	if err != nil || !strings.Contains(string(raw), "my-audit-hook") {
		t.Fatalf("user hook was not preserved: %s, %v", raw, err)
	}
	if removed, err := configureCodex(cfg); err != nil || removed {
		t.Fatalf("second configure = %v, %v", removed, err)
	}
}

func TestDeinitCodexRemovesItsRecordAndEarlierHooks(t *testing.T) {
	cfg := uxConfig(t)
	if _, err := configureCodex(cfg); err != nil {
		t.Fatal(err)
	}
	writeEarlierCodexHooks(t, cfg)
	dir, changed, err := deinitCodex(cfg)
	if err != nil || !changed || dir != codexDir(cfg) {
		t.Fatalf("deinit = %s, %v, %v", dir, changed, err)
	}
	if codexConfigured(cfg) {
		t.Fatal("integration record remains")
	}
	if present, err := sandbox.HasKeydrisHooks(cfg.CodexHooksPath); err != nil || present {
		t.Fatalf("Keydris entries remain in hooks.json: %v, %v", present, err)
	}
	if configured, err := integrationConfigured(cfg, "codex"); err != nil || configured {
		t.Fatalf("still configured after deinit: %v, %v", configured, err)
	}
	if _, changed, err := deinitCodex(cfg); err != nil || changed {
		t.Fatalf("second deinit = %v, %v", changed, err)
	}
}

func TestStatusReportsCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex is a POSIX script")
	}
	cfg := uxConfig(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := func(target string) (string, string, string) {
		for _, check := range collectStatus(cfg, target, true, false).Checks {
			if check.Name == "codex" {
				return check.State, check.Detail, check.Next
			}
		}
		return "", "", ""
	}
	if got, _, _ := state(""); got != "inactive" {
		t.Fatalf("unconfigured state = %q, want inactive", got)
	}
	if got, _, _ := state("codex"); got != "error" {
		t.Fatalf("unconfigured target state = %q, want error", got)
	}
	if _, err := configureCodex(cfg); err != nil {
		t.Fatal(err)
	}
	if got, detail, _ := state("codex"); got != "ok" || detail != "Launch with keydris codex" {
		t.Fatalf("configured state = %q (%s)", got, detail)
	}
	writeEarlierCodexHooks(t, cfg)
	if got, detail, next := state("codex"); got != "error" || !strings.Contains(detail, "earlier release") || next != "keydris init codex" {
		t.Fatalf("state with earlier hooks = %q (%s), next %q", got, detail, next)
	}
}

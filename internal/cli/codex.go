package cli

import (
	"os"
	"path/filepath"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
)

// The Codex CLI integration. `keydris codex` passes the Keydris hooks and
// their trust as -c values at each launch, after the codex it launches
// confirms it will run them. `keydris init codex` records the integration in
// ~/.keydris-data/codex. Earlier releases wrote the hooks to
// $CODEX_HOME/hooks.json instead, where Codex skipped them until a user
// trusted them in /hooks; init removes those entries.

func codexDir(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "codex")
}

// codexConfigured reports whether `keydris init codex` ran.
func codexConfigured(cfg *config.Config) bool {
	info, err := os.Stat(codexDir(cfg))
	return err == nil && info.IsDir()
}

// legacyCodexHooks reports whether hooks.json still holds Keydris hooks from
// an earlier release. Trusted ones would run beside the per-launch hooks.
func legacyCodexHooks(cfg *config.Config) (bool, error) {
	return sandbox.HasKeydrisHooks(cfg.CodexHooksPath)
}

// configureCodex records the integration and removes the Keydris hooks an
// earlier release wrote to hooks.json. It reports whether it removed any.
func configureCodex(cfg *config.Config) (bool, error) {
	removed, err := sandbox.DeconfigureCodexHooks(cfg.CodexHooksPath)
	if err != nil {
		return false, err
	}
	return removed, os.MkdirAll(codexDir(cfg), 0o700)
}

// deinitCodex removes the integration record and any Keydris hooks an earlier
// release wrote to hooks.json.
func deinitCodex(cfg *config.Config) (string, bool, error) {
	legacy, err := sandbox.DeconfigureCodexHooks(cfg.CodexHooksPath)
	if err != nil {
		return cfg.CodexHooksPath, false, err
	}
	removed, err := removeKeydrisDir(codexDir(cfg), "codex")
	return codexDir(cfg), legacy || removed, err
}

// addCodexStatus reports the Codex CLI integration and whether it counts as
// configured. It does not run codex; `keydris codex` checks the hooks at
// every launch.
func addCodexStatus(cfg *config.Config, target string, add func(string, string, string, string)) bool {
	legacy, err := legacyCodexHooks(cfg)
	if err != nil {
		add("codex", "error", "Cannot read "+cfg.CodexHooksPath, "keydris init codex")
		return false
	}
	if !codexConfigured(cfg) && !legacy {
		if target == "" {
			add("codex", "inactive", "Not configured", "")
		} else {
			add("codex", "error", "Not configured", "keydris init codex")
		}
		return false
	}
	if legacy {
		add("codex", "error", "Keydris hooks from an earlier release remain in "+cfg.CodexHooksPath, "keydris init codex")
		return true
	}
	if _, err := hostenv.ResolveCommand("codex"); err != nil {
		add("codex", "warning", "Configured; "+err.Error(), "Install codex for this runtime or correct PATH")
		return true
	}
	add("codex", "ok", "Launch with keydris codex", "")
	addSkillStatus(cfg, "codex", add)
	return true
}

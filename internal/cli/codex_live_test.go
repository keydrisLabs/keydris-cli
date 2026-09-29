package cli

import (
	"os"
	"strings"
	"testing"

	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
)

// TestGovernedCodexLaunchLive runs a real codex with the arguments `keydris
// codex` builds and requires every Keydris hook trusted and enabled, including
// when the user passes -c after the subcommand. It is skipped unless
// KEYDRIS_CODEX_LIVE=1, uses codex from PATH or the Codex app's bundled codex,
// and never touches ~/.codex or the network.
func TestGovernedCodexLaunchLive(t *testing.T) {
	if os.Getenv("KEYDRIS_CODEX_LIVE") != "1" {
		t.Skip("set KEYDRIS_CODEX_LIVE=1 to run against an installed codex")
	}
	codex, err := hostenv.ResolveCommand("codex")
	if err != nil {
		app, appErr := findCodexDesktopApp()
		if appErr != nil {
			t.Fatalf("no codex on PATH (%v) or in the Codex app (%v)", err, appErr)
		}
		codex = app.Codex
	}
	overrides, err := governedCodexOverrides(codex, codexHooks("'/usr/local/bin/keydris'"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"app-server"},
		{"-c", `model="o3"`, "app-server"},
		// Codex keeps only this group; the one before app-server is dropped.
		{"-c", `model="o3"`, "app-server", "-c", "features.plugins=false"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			hooks, err := runCodexHookListing(codex, codexCommandArgs(args, overrides))
			if err != nil {
				t.Fatal(err)
			}
			if err := keydrisHooksReady(hooks, 3); err != nil {
				t.Fatal(err)
			}
		})
	}
}

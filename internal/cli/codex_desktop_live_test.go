package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodexDesktopShimLive runs the installed app's codex through the shim the
// way the app does. It is skipped unless KEYDRIS_CODEX_DESKTOP_LIVE=1, and it
// never touches ~/.codex or the network.
func TestCodexDesktopShimLive(t *testing.T) {
	if os.Getenv("KEYDRIS_CODEX_DESKTOP_LIVE") != "1" {
		t.Skip("set KEYDRIS_CODEX_DESKTOP_LIVE=1 to run against the installed Codex app")
	}
	app, err := findCodexDesktopApp()
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(t.TempDir(), "bin", "codex")
	// Lists every app invocation through the shim and requires all three
	// hooks trusted and enabled.
	if err := prepareCodexDesktopShim(shim, app.Codex, codexHooks("'/usr/local/bin/keydris'")); err != nil {
		t.Fatal(err)
	}

	// config.toml is the user's: /hooks records a hook turned off there, and
	// anything that can write ~/.codex can do the same. The launch still wins.
	for _, args := range codexDesktopInvocations {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			hooks, err := runCodexHookListing(shim, args)
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			config := "[features]\nhooks = false\n"
			for _, hook := range sessionFlagHooks(hooks) {
				config += fmt.Sprintf("\n[hooks.state.%q]\nenabled = false\ntrusted_hash = \"sha256:0\"\n", hook.Key)
			}
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			hooks, err = runCodexHookListingIn(home, shim, args)
			if err != nil {
				t.Fatal(err)
			}
			if err := keydrisHooksReady(hooks, 3); err != nil {
				t.Fatal(err)
			}
		})
	}
}

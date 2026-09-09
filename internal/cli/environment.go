package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
)

func environmentChecks(target string) []healthCheck {
	e := hostenv.Current()
	checks := []healthCheck{}
	if err := e.Validate(); err != nil {
		return []healthCheck{{"Runtime", "error", err.Error(), "Use Linux Keydris in WSL2; check wsl.exe --list --verbose from Windows"}}
	}
	if e.WSL == "2" {
		checks = append(checks, healthCheck{"Runtime", "ok", "WSL2 (" + e.Distro + "); Linux CLI and distribution-local state", ""})
	}
	if e.VSCode {
		detail := "VS Code terminal; start Claude with claude or Codex with keydris codex"
		if e.WSL != "" {
			detail += " in this WSL distribution"
		}
		checks = append(checks, healthCheck{"Terminal", "ok", detail + "; sidebar support is separate", ""})
	}
	if target == "claude-code" && e.OS == "linux" {
		missing := false
		for _, command := range []string{"bwrap", "socat"} {
			path, err := hostenv.ResolveCommand(command)
			if err != nil {
				checks = append(checks, healthCheck{"Sandbox", "error", fmt.Sprintf("Linux %s is unavailable: %v", command, err), "Install bubblewrap and socat in the Linux distribution, then rerun keydris doctor"})
				missing = true
			} else if command == "bwrap" {
				// Probe namespace creation without network access or writable mounts.
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err := exec.CommandContext(ctx, path, "--unshare-user", "--unshare-net", "--ro-bind", "/", "/", "--", "/bin/true").Run()
				cancel()
				if err != nil {
					checks = append(checks, healthCheck{"Sandbox", "error", "Bubblewrap cannot create the required namespaces", "Review Linux namespace/AppArmor restrictions using the Claude sandbox documentation; keep sandbox enforcement enabled"})
					missing = true
				}
			}
		}
		if !missing {
			checks = append(checks, healthCheck{"Sandbox", "ok", "bubblewrap, socat and Linux namespace probe available", ""})
		}
	}
	return checks
}

func checkInitEnvironment(target string) error {
	for _, check := range environmentChecks(target) {
		newUI(os.Stdout).row(check.State, check.Name, check.Detail)
		if check.State == "error" {
			return fmt.Errorf("%s", check.Next)
		}
	}
	return nil
}

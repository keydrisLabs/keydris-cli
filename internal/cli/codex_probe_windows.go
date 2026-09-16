//go:build windows

package cli

import (
	"context"
	"os/exec"
	"syscall"
)

func codexProbeShell(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

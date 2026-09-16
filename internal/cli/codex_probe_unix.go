//go:build !windows

package cli

import (
	"context"
	"os/exec"
)

func codexProbeShell(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUXResetRefusesWindowsJunction(t *testing.T) {
	cfg := uxConfig(t)
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	junction := filepath.Join(cfg.DataDir, "junction")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target)
	configureDetachedProcess(command)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v: %s", err, out)
	}
	defer os.Remove(junction)
	if _, err := planReset(cfg, true); err == nil {
		t.Fatal("reset accepted a Windows junction")
	}
}

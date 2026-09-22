package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// Reporting is best-effort and independent for each runtime: a broken file
// must not clear its last good snapshot or hide the other runtime's entries.
func reportMCPInventories(cfg *config.Config, w io.Writer) {
	client, err := mTLSClient(cfg)
	if err != nil {
		fmt.Fprintln(w, "keydris: MCP inventory could not authenticate; keeping the previous report")
		return
	}
	defer client.CloseIdleConnections()
	sources := []struct {
		runtime string
		path    string
		read    func(string) ([]runtimecontract.MCPInventoryEntry, error)
	}{
		{"claude_code", cfg.ClaudeMcpConfigPath, sandbox.ReadClaudeMCPInventory},
		{"codex", cfg.CodexConfigPath, sandbox.ReadCodexMCPInventory},
	}
	for _, source := range sources {
		observedAt := time.Now().UTC().Format(time.RFC3339Nano)
		entries, err := source.read(source.path)
		if err != nil {
			fmt.Fprintf(w, "keydris: %s MCP inventory unavailable; keeping the previous report\n", source.runtime)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = runtimecontract.ReportMCPInventory(ctx, client, cfg.ControlMTLSURL, runtimecontract.MCPInventoryReport{
			SchemaVersion: runtimecontract.SchemaVersion, Runtime: source.runtime, ObservedAt: observedAt, Entries: entries,
		})
		cancel()
		if err != nil {
			fmt.Fprintf(w, "keydris: %s MCP inventory delivery failed; will retry on the next session\n", source.runtime)
		}
	}
	reportDesktopMCPInventory(cfg, client, w)
}

func reportDesktopMCPInventory(cfg *config.Config, client *http.Client, w io.Writer) {
	configPath, roots := desktopMCPInventoryPaths()
	if configPath == "" && len(roots) == 0 {
		return
	}
	entries, err := sandbox.ReadDesktopMCPInventory(configPath, roots)
	if err != nil {
		fmt.Fprintln(w, "keydris: claude_desktop MCP inventory unavailable; keeping the previous report")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = runtimecontract.ReportMCPInventory(ctx, client, cfg.ControlMTLSURL, runtimecontract.MCPInventoryReport{
		SchemaVersion: runtimecontract.SchemaVersion, Runtime: agentRuntimeClaudeDesktop, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Entries: entries,
	})
	cancel()
	if err != nil {
		fmt.Fprintln(w, "keydris: claude_desktop MCP inventory delivery failed; will retry on the next session")
	}
}

func desktopMCPInventoryPaths() (string, []string) {
	configPath := os.Getenv("KEYDRIS_CLAUDE_DESKTOP_MCP_CONFIG")
	var roots []string
	if extra := os.Getenv("KEYDRIS_CLAUDE_DESKTOP_PLUGIN_ROOTS"); extra != "" {
		roots = append(roots, filepath.SplitList(extra)...)
	}
	if runtime.GOOS != "darwin" || (configPath != "" && len(roots) > 0) {
		return configPath, roots
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return configPath, roots
	}
	if configPath == "" {
		configPath = filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	}
	if len(roots) == 0 {
		roots = []string{
			"/Library/Application Support/Claude/org-plugins",
			filepath.Join(home, "Library", "Application Support", "Claude", "Claude Extensions"),
		}
	}
	return configPath, roots
}

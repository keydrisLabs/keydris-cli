package cli

import (
	"context"
	"fmt"
	"io"
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
}

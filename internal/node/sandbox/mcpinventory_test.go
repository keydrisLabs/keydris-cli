package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeInventoryIncludesManualEntriesAndRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	content := `{"keydrisManagedMcpServers":["managed"],"mcpServers":{"managed":{"type":"http","url":"https://mcp.example.com/mcp"},"manual":{"url":"https://user:password@example.com/mcp?token=secret#fragment","headers":{"Authorization":"secret"}},"local":{"command":"secret-command","args":["secret-argument"],"env":{"TOKEN":"secret"}}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadClaudeMCPInventory(path)
	if err != nil || len(entries) != 3 {
		t.Fatalf("inventory: %v, %v", entries, err)
	}
	encoded, _ := json.Marshal(entries)
	for _, secret := range []string{"password", "secret", "Authorization", "fragment", "TOKEN"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if entries[0].Transport != "stdio" || !entries[1].Managed || !entries[2].EndpointRedacted {
		t.Fatalf("incorrect metadata: %+v", entries)
	}
}

func TestCodexInventoryReadsOnlyServerMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "# keydris-managed-mcp-servers: managed\n[mcp_servers.managed]\nurl = \"https://example.com/mcp\"\n[mcp_servers.managed.http_headers]\nAuthorization = \"secret\"\n[mcp_servers.local]\ncommand = 'secret-command'\nenabled = false\n[mcp_servers.local.env]\nTOKEN = 'secret'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadCodexMCPInventory(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("inventory: %+v, %v", entries, err)
	}
	if entries[0].Enabled || entries[0].Transport != "stdio" || !entries[1].Managed {
		t.Fatalf("incorrect metadata: %+v", entries)
	}
	encoded, _ := json.Marshal(entries)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("secret metadata leaked")
	}
}

func TestInvalidConfigurationIsNotAnEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadClaudeMCPInventory(path); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if err := os.WriteFile(path, []byte("[mcp_servers.test]\nurl = invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCodexMCPInventory(path); err == nil {
		t.Fatal("invalid TOML metadata accepted")
	}
	entries, err := ReadClaudeMCPInventory(filepath.Join(dir, "missing"))
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatal("missing config must be a complete empty snapshot")
	}
}

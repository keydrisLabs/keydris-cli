package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
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

// TestInventoryRejectsUnsupportedMetadata pins the fail-closed reader contract:
// metadata the reader cannot interpret returns an error so the caller keeps the
// last good snapshot instead of uploading a partial or empty one.
func TestInventoryRejectsUnsupportedMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	for _, tc := range []struct {
		name    string
		content string
		read    func(string) ([]runtimecontract.MCPInventoryEntry, error)
	}{
		{"claude servers not an object", `{"mcpServers":"nope"}`, ReadClaudeMCPInventory},
		{"claude entry not an object", `{"mcpServers":{"bad":"string"}}`, ReadClaudeMCPInventory},
		{"claude endpoint not a string", `{"mcpServers":{"bad":{"url":42}}}`, ReadClaudeMCPInventory},
		{"codex inline table", "[mcp_servers]\n", ReadCodexMCPInventory},
		{"codex duplicate table", "[mcp_servers.a]\n[mcp_servers.a]\n", ReadCodexMCPInventory},
		{"codex enabled flag not a boolean", "[mcp_servers.a]\nenabled = \"yes\"\n", ReadCodexMCPInventory},
		{"codex unsupported value syntax", "[mcp_servers.a]\nurl = bare\n", ReadCodexMCPInventory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.read(path); err == nil {
				t.Fatalf("accepted unsupported metadata %q", tc.content)
			}
		})
	}
}

// TestInventoryBoundsAndEndpointRedaction pins the wire-size and secret
// defenses: oversized inventories and unsafe names are refused, and endpoints
// that cannot be normalized safely are dropped rather than sent raw.
func TestInventoryBoundsAndEndpointRedaction(t *testing.T) {
	oversized := make([]runtimecontract.MCPInventoryEntry, 257)
	for i := range oversized {
		oversized[i].Name = fmt.Sprintf("server-%03d", i)
	}
	if _, err := validateInventory(oversized); err == nil {
		t.Fatal("inventory over 256 entries accepted")
	}
	for _, name := range []string{"", "   ", strings.Repeat("x", 257), "bad\x01name"} {
		if _, err := validateInventory([]runtimecontract.MCPInventoryEntry{{Name: name}}); err == nil {
			t.Fatalf("invalid entry name %q accepted", name)
		}
	}
	for _, raw := range []string{"not a url", "ftp://example.com/mcp", "https:///missing-host"} {
		entry := newInventoryEntry("sum", false)
		setInventoryEndpoint(&entry, raw)
		if !entry.EndpointRedacted || entry.Endpoint != nil || entry.Transport != "http" {
			t.Fatalf("endpoint %q = %+v, want redacted HTTP", raw, entry)
		}
	}
	entry := newInventoryEntry("sum", false)
	setInventoryEndpoint(&entry, "https://example.com/"+strings.Repeat("p", 2048))
	if !entry.EndpointRedacted || entry.Endpoint != nil {
		t.Fatalf("overlong endpoint = %+v, want redacted", entry)
	}
}

func TestDesktopInventoryIncludesUserAndPluginEntriesAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "desktop.json")
	user := `{"mcpServers":{"user":{"type":"http","url":"https://user:password@example.com/mcp?token=secret#fragment","headers":{"Authorization":"secret"}}}}`
	if err := os.WriteFile(configPath, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginRoot := filepath.Join(dir, "plugins")
	pluginDir := filepath.Join(pluginRoot, "vendor", "tool")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := `{"mcpServers":{"local":{"command":"secret-command","args":["secret-argument"],"env":{"TOKEN":"secret"}},"remote":{"type":"http","url":"https://plugin.example.com/mcp","headers":{"Authorization":"secret"}}}}`
	if err := os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), []byte(plugin), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadDesktopMCPInventory(configPath, []string{pluginRoot})
	if err != nil || len(entries) != 3 {
		t.Fatalf("inventory: %v, %v", entries, err)
	}
	encoded, _ := json.Marshal(entries)
	for _, secret := range []string{"password", "secret", "Authorization", "fragment", "TOKEN", "secret-command"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if entries[0].Transport != "stdio" || !entries[0].Managed || !entries[1].Managed || !entries[2].EndpointRedacted {
		t.Fatalf("incorrect metadata: %+v", entries)
	}
}

func TestDesktopInventoryRejectsInvalidUserConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDesktopMCPInventory(path, nil); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestDesktopInventoryMissingPathsAreEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	entries, err := ReadDesktopMCPInventory(filepath.Join(dir, "missing.json"), []string{filepath.Join(dir, "missing-plugins")})
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatal("missing config must be a complete empty snapshot")
	}
	entries, err = ReadDesktopMCPInventory("", nil)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatal("empty inputs must be a complete empty snapshot")
	}
}

func TestDesktopInventoryRejectsInvalidPluginConfig(t *testing.T) {
	dir := t.TempDir()
	pluginRoot := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".mcp.json"), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDesktopMCPInventory(filepath.Join(dir, "missing.json"), []string{pluginRoot}); err == nil {
		t.Fatal("invalid plugin JSON accepted")
	}
}

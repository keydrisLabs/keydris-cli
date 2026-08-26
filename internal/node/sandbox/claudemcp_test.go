package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	config := map[string]any{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return config
}

func servers(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	entries, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return entries
}

func TestConfigureMcpServersWritesGovernedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")

	if err := ConfigureMcpServers(path, []McpServer{
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	entry, ok := servers(t, readConfig(t, path))["github-mcp"].(map[string]any)
	if !ok {
		t.Fatal("github-mcp not written")
	}
	if entry["type"] != "http" ||
		entry["url"] != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("entry = %+v", entry)
	}
	// No credential belongs in the config: the data plane intercepts and Keydris
	// injects the upstream token.
	if _, present := entry["headers"]; present {
		t.Fatal("entry carries headers")
	}
}

func TestConfigureMcpServersPreservesUserEntriesAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	seed := map[string]any{
		"projects": map[string]any{"/work": map[string]any{"history": []any{"a"}}},
		"mcpServers": map[string]any{
			"my-own": map[string]any{"type": "http", "url": "https://mine.test/"},
		},
	}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ConfigureMcpServers(path, []McpServer{
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	config := readConfig(t, path)
	entries := servers(t, config)
	if _, ok := entries["my-own"]; !ok {
		t.Fatal("hand-added server was clobbered")
	}
	if _, ok := entries["github-mcp"]; !ok {
		t.Fatal("governed server missing")
	}
	if _, ok := config["projects"]; !ok {
		t.Fatal("unrelated config was dropped")
	}
}

func TestConfigureMcpServersDropsServersNoLongerGoverned(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := ConfigureMcpServers(path, []McpServer{
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
		{Name: "other-mcp", URL: "https://other.test/mcp"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// A narrowed policy must not leave a stale server the agent can still reach.
	if err := ConfigureMcpServers(path, []McpServer{
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
	}); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}

	entries := servers(t, readConfig(t, path))
	if _, ok := entries["other-mcp"]; ok {
		t.Fatal("ungoverned server left behind")
	}
	if _, ok := entries["github-mcp"]; !ok {
		t.Fatal("governed server removed")
	}
}

func TestRemoveManagedMcpServersLeavesUserEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	seed := map[string]any{
		"mcpServers": map[string]any{
			"my-own": map[string]any{"type": "http", "url": "https://mine.test/"},
		},
	}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureMcpServers(path, []McpServer{
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	if err := RemoveManagedMcpServers(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	entries := servers(t, readConfig(t, path))
	if _, ok := entries["github-mcp"]; ok {
		t.Fatal("managed server survived deinit")
	}
	if _, ok := entries["my-own"]; !ok {
		t.Fatal("deinit removed a hand-added server")
	}
}

func TestConfigureMcpServersRefusesMalformedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Overwriting would destroy the user's own servers and project history.
	if err := ConfigureMcpServers(path, nil); err == nil {
		t.Fatal("malformed config was overwritten instead of refused")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{not json" {
		t.Fatalf("file was modified: %s", data)
	}
}

func TestConfigureMcpServersCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", ".claude.json")

	if err := ConfigureMcpServers(path, []McpServer{
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, ok := servers(t, readConfig(t, path))["github-mcp"]; !ok {
		t.Fatal("server not written to a fresh config")
	}
}

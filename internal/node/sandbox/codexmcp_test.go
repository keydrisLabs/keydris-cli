package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCodexConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("seed config: %v", err)
		}
	}
	return path
}

func readCodexConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

func TestConfigureCodexMcpServersWritesServerAndFeatureFlag(t *testing.T) {
	path := writeCodexConfig(t, "")

	err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	got := readCodexConfig(t, path)
	// Streamable HTTP is behind the flag; without it Codex ignores a url server.
	if !strings.Contains(got, "experimental_use_rmcp_client = true") {
		t.Fatalf("feature flag missing:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.slack-mcp]") {
		t.Fatalf("server table missing:\n%s", got)
	}
	if !strings.Contains(got, `url = "https://mcp.slack.com/mcp"`) {
		t.Fatalf("server url missing:\n%s", got)
	}
	// No credential belongs in the config: the data plane intercepts and Keydris
	// injects the upstream token.
	if strings.Contains(got, "bearer_token") {
		t.Fatalf("entry carries a credential:\n%s", got)
	}
}

func TestConfigureCodexMcpServersPreservesUserContent(t *testing.T) {
	seed := `# Keydris still owns the identity lifecycle through ` + "`keydris codex`" + `.
sandbox_mode = "workspace-write"
approval_policy = "untrusted"

[sandbox_workspace_write]
# Permit sandboxed commands to reach the authenticated Keydris proxy.
network_access = true

[mcp_servers.mine]
command = "my-server"
`
	path := writeCodexConfig(t, seed)

	err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	got := readCodexConfig(t, path)
	for _, keep := range []string{
		"# Keydris still owns the identity lifecycle",
		`sandbox_mode = "workspace-write"`,
		`approval_policy = "untrusted"`,
		"[sandbox_workspace_write]",
		"# Permit sandboxed commands to reach the authenticated Keydris proxy.",
		"network_access = true",
		"[mcp_servers.mine]",
		`command = "my-server"`,
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("dropped user content %q:\n%s", keep, got)
		}
	}
}

func TestConfigureCodexMcpServersEditsAnExistingFeaturesTable(t *testing.T) {
	// A second `[features]` table would make the file invalid TOML, and Codex
	// would then read none of it.
	path := writeCodexConfig(t, "[features]\nsome_other_flag = true\n")

	err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	got := readCodexConfig(t, path)
	if strings.Count(got, "[features]") != 1 {
		t.Fatalf("features table duplicated:\n%s", got)
	}
	if !strings.Contains(got, "some_other_flag = true") {
		t.Fatalf("dropped the user's flag:\n%s", got)
	}
	if !strings.Contains(got, "experimental_use_rmcp_client = true") {
		t.Fatalf("feature flag missing:\n%s", got)
	}
}

func TestConfigureCodexMcpServersDropsAServerThePolicyNoLongerGoverns(t *testing.T) {
	path := writeCodexConfig(t, "")
	if err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"},
		{Name: "github-mcp", URL: "https://api.githubcopilot.com/mcp/"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	if err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"},
	}); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}

	got := readCodexConfig(t, path)
	if strings.Contains(got, "github-mcp") {
		t.Fatalf("stale server survived a narrowed policy:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.slack-mcp]") {
		t.Fatalf("governed server missing:\n%s", got)
	}
}

func TestConfigureCodexMcpServersIsStableAcrossRepeatedWrites(t *testing.T) {
	path := writeCodexConfig(t, "sandbox_mode = \"workspace-write\"\n")
	servers := []McpServer{{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"}}

	if err := ConfigureCodexMcpServers(path, servers); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := readCodexConfig(t, path)
	if err := ConfigureCodexMcpServers(path, servers); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}

	// Every session hook rewrites this file; drifting output would churn the
	// user's config on each start.
	if second := readCodexConfig(t, path); second != first {
		t.Fatalf("not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if strings.Count(first, "[mcp_servers.slack-mcp]") != 1 {
		t.Fatalf("server table duplicated:\n%s", first)
	}
}

func TestConfigureCodexMcpServersIgnoresBracketsInsideMultilineStrings(t *testing.T) {
	seed := "notes = \"\"\"\n[mcp_servers.not-a-table]\n\"\"\"\n"
	path := writeCodexConfig(t, seed)

	if err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "not-a-table", URL: "https://mcp.example.com/mcp"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	got := readCodexConfig(t, path)
	// The line inside the string is text, not a table Keydris may rewrite.
	if !strings.Contains(got, "notes = \"\"\"\n[mcp_servers.not-a-table]\n\"\"\"") {
		t.Fatalf("multi-line string was rewritten:\n%s", got)
	}
}

func TestRemoveManagedCodexMcpServers(t *testing.T) {
	path := writeCodexConfig(t, "[mcp_servers.mine]\ncommand = \"my-server\"\n")
	if err := ConfigureCodexMcpServers(path, []McpServer{
		{Name: "slack-mcp", URL: "https://mcp.slack.com/mcp"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	changed, err := RemoveManagedCodexMcpServers(path)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !changed {
		t.Fatal("expected a change")
	}

	got := readCodexConfig(t, path)
	if strings.Contains(got, "slack-mcp") {
		t.Fatalf("managed server survived:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.mine]") {
		t.Fatalf("removed the user's own server:\n%s", got)
	}
}

func TestRemoveManagedCodexMcpServersOnMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	changed, err := RemoveManagedCodexMcpServers(path)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if changed {
		t.Fatal("reported a change for a file that does not exist")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created a config file that did not exist")
	}
}

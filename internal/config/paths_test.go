package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/platform"
)

func TestConfigPathsResolveAgainstFileInsteadOfWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, ".keydris.toml")
	t.Setenv("KEYDRIS_DATA_DIR", "")
	if err := os.WriteFile(source, []byte("data_dir = 'my data'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loadToml(source)
	if got, want := os.Getenv("KEYDRIS_DATA_DIR"), filepath.Join(dir, "my data"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Environment path overrides must be absolute so a hook launched in another
// project sees the same identity, CA and socket. The config-file loader already
// resolves relative values; this guards the raw environment path.
func TestValidatePathsRequiresAbsoluteRuntimeOverrides(t *testing.T) {
	if err := platform.Current().Validate(); err != nil {
		t.Skipf("host runtime is not a supported environment: %v", err)
	}
	dir := t.TempDir()
	absolute := func(name string) string { return filepath.Join(dir, name) }
	valid := func() *Config {
		return &Config{
			DataDir:             absolute("data"),
			IdentityDir:         absolute("identity"),
			CAPath:              absolute("ca.crt"),
			CAKeyPath:           absolute("ca.key"),
			CABundlePath:        absolute("ca-bundle.crt"),
			SessionSocket:       absolute("session.sock"),
			SessionAuthFile:     absolute("session.auth"),
			ClaudeSettingsPath:  absolute("claude/settings.json"),
			ClaudeMcpConfigPath: absolute("claude/mcp.json"),
			CodexHooksPath:      absolute("codex/hooks.json"),
			SigningKeyPath:      absolute("signing.key"),
			LedgerPath:          absolute("evidence.jsonl"),
			ClientCAPath:        absolute("client-ca.crt"),
			ClientCAKeyPath:     absolute("client-ca.key"),
		}
	}
	if err := valid().ValidatePaths(); err != nil {
		t.Fatalf("absolute paths rejected: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"data dir", func(c *Config) { c.DataDir = "relative/data" }},
		{"identity dir", func(c *Config) { c.IdentityDir = "relative/identity" }},
		{"ca bundle", func(c *Config) { c.CABundlePath = "ca-bundle.crt" }},
		{"claude settings", func(c *Config) { c.ClaudeSettingsPath = "settings.json" }},
		{"codex hooks", func(c *Config) { c.CodexHooksPath = "hooks.json" }},
		{"ledger", func(c *Config) { c.LedgerPath = "evidence.jsonl" }},
		{"newline", func(c *Config) { c.LedgerPath = absolute("evidence.jsonl") + "\n" }},
		{"mtls server ca", func(c *Config) { c.MTLSServerCA = "mtls-ca.crt" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(cfg)
			if err := cfg.ValidatePaths(); err == nil {
				t.Fatal("relative override accepted")
			}
		})
	}
}

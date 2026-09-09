package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keydrisLabs/keydris-cli/internal/platform"
)

// ValidatePaths refuses working-directory-dependent runtime state. File-based
// relative settings are resolved against the config file; environment path
// overrides must be absolute so a hook launched in another project sees the
// same identity, CA and socket.
func (c *Config) ValidatePaths() error {
	environment := platform.Current()
	if err := environment.Validate(); err != nil {
		return err
	}
	for _, item := range []struct{ name, value string }{
		{"KEYDRIS_DATA_DIR", c.DataDir}, {"KEYDRIS_IDENTITY_DIR", c.IdentityDir},
		{"KEYDRIS_CA_PATH", c.CAPath}, {"KEYDRIS_CA_KEY_PATH", c.CAKeyPath},
		{"KEYDRIS_CA_BUNDLE_PATH", c.CABundlePath}, {"KEYDRIS_SESSION_SOCKET", c.SessionSocket},
		{"KEYDRIS_SESSION_AUTH_FILE", c.SessionAuthFile}, {"KEYDRIS_CLAUDE_SETTINGS", c.ClaudeSettingsPath},
		{"KEYDRIS_CLAUDE_MCP_CONFIG", c.ClaudeMcpConfigPath}, {"KEYDRIS_CODEX_HOOKS", c.CodexHooksPath},
		{"KEYDRIS_SIGNING_KEY", c.SigningKeyPath}, {"KEYDRIS_LEDGER_PATH", c.LedgerPath},
		{"KEYDRIS_CLIENT_CA_PATH", c.ClientCAPath}, {"KEYDRIS_CLIENT_CA_KEY_PATH", c.ClientCAKeyPath},
	} {
		if !filepath.IsAbs(item.value) || strings.ContainsAny(item.value, "\x00\r\n") {
			return fmt.Errorf("%s must be an absolute path (got %q)", item.name, item.value)
		}
		if err := environment.ValidatePath(item.name, item.value); err != nil {
			return err
		}
	}
	if c.MTLSServerCA != "" && !filepath.IsAbs(c.MTLSServerCA) {
		return fmt.Errorf("KEYDRIS_MTLS_SERVER_CA must be an absolute path")
	}
	return nil
}
func filePathValue(key, value, source string) string {
	switch key {
	case "KEYDRIS_DATA_DIR", "KEYDRIS_IDENTITY_DIR", "KEYDRIS_CA_PATH", "KEYDRIS_CA_KEY_PATH",
		"KEYDRIS_CA_BUNDLE_PATH", "KEYDRIS_SESSION_SOCKET", "KEYDRIS_SESSION_AUTH_FILE",
		"KEYDRIS_CLAUDE_SETTINGS", "KEYDRIS_CLAUDE_MCP_CONFIG", "KEYDRIS_CODEX_HOOKS",
		"KEYDRIS_SIGNING_KEY", "KEYDRIS_LEDGER_PATH", "KEYDRIS_CLIENT_CA_PATH",
		"KEYDRIS_CLIENT_CA_KEY_PATH", "KEYDRIS_MTLS_SERVER_CA", "KEYDRIS_GRANTS_SEED":
	default:
		return value
	}
	if value == "" {
		return value
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
		}
	}
	if !filepath.IsAbs(value) {
		if base, err := filepath.Abs(source); err == nil {
			return filepath.Clean(filepath.Join(filepath.Dir(base), value))
		}
	}
	return filepath.Clean(value)
}

package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// loadToml applies a minimal .keydris.toml to the environment. Each `key = val`
// (optionally under a `[keydris]` table) maps to KEYDRIS_<UPPER(KEY)> and is set
// only if not already present, so the precedence is:
//
//	process env  >  .env  >  ./.keydris.toml  >  ~/.keydris.toml  >  defaults
//
// This is a tiny flat parser (strings/ints, # comments), not full TOML.
func loadToml(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" {
			continue
		}
		envKey := "KEYDRIS_" + strings.ToUpper(key)
		if os.Getenv(envKey) == "" {
			_ = os.Setenv(envKey, val)
		}
	}
}

// loadLayeredFiles seeds the environment from .env then the layered
// .keydris.toml files (cwd first, then home), each only filling unset keys.
func loadLayeredFiles() {
	loadDotEnv(".env")
	loadToml(".keydris.toml")
	if home, err := os.UserHomeDir(); err == nil {
		loadToml(filepath.Join(home, ".keydris.toml"))
	}
}

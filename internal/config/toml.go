package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// loadToml applies a minimal .keydris.toml to the environment. Each `key = val`
// (optionally under a `[keydris]` table) maps to KEYDRIS_<UPPER(KEY)> and is set
// only if not already present. Process environment values always win; the
// trusted user file is loaded by default, while project files require explicit
// opt-in.
//
//	process env > ~/.keydris.toml > opted-in project files > defaults
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

// loadLayeredFiles seeds the environment from trusted user configuration.
// Project-local configuration can redirect authentication and control-plane
// traffic, so it is ignored unless the caller explicitly opted in through the
// process environment before Load was called.
func loadLayeredFiles() {
	trustProject := os.Getenv("KEYDRIS_TRUST_PROJECT_CONFIG") == "1"
	if home, err := os.UserHomeDir(); err == nil {
		loadToml(filepath.Join(home, ".keydris.toml"))
	}
	if trustProject {
		loadDotEnv(".env")
		loadToml(".keydris.toml")
	}
}

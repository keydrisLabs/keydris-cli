package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var tomlKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

type layeredConfig struct {
	Sources             map[string]string
	ManagedPath         string
	ManagedLoaded       bool
	ManagedSettingCount int
	ManagedErr          error
}

// loadToml applies a trusted user .keydris.toml to the environment. Each
// `key = val` (optionally under a `[keydris]` table) maps to
// KEYDRIS_<UPPER(KEY)> and is set only if not already present.
func loadToml(path string) {
	_, _ = loadTomlLayer(path, "user", false, false, nil)
}

// loadTomlLayer loads one flat TOML layer. Managed configuration is strict and
// authoritative for every key it contains. User and explicitly trusted project
// files retain the historical lenient parser and cannot replace an earlier
// layer.
func loadTomlLayer(path, source string, force, strict bool, sources map[string]string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	seen := map[string]struct{}{}
	type setting struct{ envKey, value string }
	var settings []setting
	sc := bufio.NewScanner(f)
	for lineNumber := 1; sc.Scan(); lineNumber++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if strict && line != "[keydris]" {
				return 0, fmt.Errorf("line %d: only the [keydris] table is supported", lineNumber)
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || !tomlKeyPattern.MatchString(key) {
			if strict {
				return 0, fmt.Errorf("line %d: invalid managed setting", lineNumber)
			}
			continue
		}
		envKey := "KEYDRIS_" + strings.ToUpper(key)
		if _, duplicate := seen[envKey]; duplicate && strict {
			return 0, fmt.Errorf("line %d: duplicate setting %s", lineNumber, key)
		}
		seen[envKey] = struct{}{}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		settings = append(settings, setting{envKey, filePathValue(envKey, val, path)})
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}

	loaded := 0
	for _, item := range settings {
		lockedByManaged := sources != nil && strings.HasPrefix(sources[item.envKey], "managed: ")
		if !force && (os.Getenv(item.envKey) != "" || lockedByManaged) {
			continue
		}
		if err := os.Setenv(item.envKey, item.value); err != nil {
			return 0, err
		}
		if sources != nil {
			sources[item.envKey] = source
		}
		loaded++
	}
	return loaded, nil
}

// loadLayeredFiles seeds the environment from managed, process, user and
// explicitly trusted project configuration. A managed setting cannot be
// replaced by the invoking user's environment or files.
//
//	managed system file > process env > ~/.keydris.toml > opted-in project files > defaults
func loadLayeredFiles() layeredConfig {
	result := layeredConfig{
		Sources:     map[string]string{},
		ManagedPath: defaultManagedConfigPath(),
	}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "KEYDRIS_") {
			result.Sources[key] = "environment"
		}
	}

	// Project trust is an explicit process choice. A file cannot enable its own
	// loading by setting KEYDRIS_TRUST_PROJECT_CONFIG.
	trustProject := os.Getenv("KEYDRIS_TRUST_PROJECT_CONFIG") == "1"
	if err := validateManagedConfig(result.ManagedPath, 0); err != nil {
		result.ManagedErr = err
	} else if _, err := os.Stat(result.ManagedPath); err == nil {
		count, loadErr := loadTomlLayer(
			result.ManagedPath,
			"managed: "+result.ManagedPath,
			true,
			true,
			result.Sources,
		)
		result.ManagedSettingCount = count
		result.ManagedLoaded = loadErr == nil
		if loadErr != nil {
			result.ManagedErr = fmt.Errorf("managed config %s: %w", result.ManagedPath, loadErr)
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		_, _ = loadTomlLayer(filepath.Join(home, ".keydris.toml"), "user: ~/.keydris.toml", false, false, result.Sources)
	}
	if trustProject {
		loadDotEnv(".env")
		for _, entry := range os.Environ() {
			key, _, ok := strings.Cut(entry, "=")
			if ok && strings.HasPrefix(key, "KEYDRIS_") {
				if _, exists := result.Sources[key]; !exists {
					result.Sources[key] = "project: .env"
				}
			}
		}
		_, _ = loadTomlLayer(".keydris.toml", "project: .keydris.toml", false, false, result.Sources)
	}
	return result
}

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// McpServer is one entry Claude Code dials. No credential: the data plane
// intercepts the connection and Keydris injects the upstream token, so the
// config carries only where to go.
type McpServer struct {
	Name string
	URL  string
}

// ConfigureMcpServers merges Keydris-governed MCP servers into Claude Code's
// user-scope config (~/.claude.json → mcpServers). Servers live there, NOT in
// settings.json where the sandbox and hooks are written.
//
// Entries the user added by hand are preserved; only names Keydris manages are
// replaced, and names it previously managed but no longer governs are removed so
// a narrowed policy does not leave a stale server behind.
func ConfigureMcpServers(path string, servers []McpServer) error {
	config, err := readJSONObject(path)
	if err != nil {
		return err
	}

	existing, _ := config["mcpServers"].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	managed := managedNames(config)

	governed := map[string]bool{}
	for _, server := range servers {
		governed[server.Name] = true
		existing[server.Name] = map[string]any{
			"type": "http",
			"url":  server.URL,
		}
	}
	for _, name := range managed {
		if !governed[name] {
			delete(existing, name)
		}
	}

	config["mcpServers"] = existing
	config[managedKey] = sortedNames(governed)
	return writeJSONObject(path, config)
}

// RemoveManagedMcpServers drops every entry Keydris added, leaving the user's own.
func RemoveManagedMcpServers(path string) error {
	config, err := readJSONObject(path)
	if err != nil {
		return err
	}
	existing, _ := config["mcpServers"].(map[string]any)
	for _, name := range managedNames(config) {
		delete(existing, name)
	}
	if len(existing) == 0 {
		delete(config, "mcpServers")
	} else {
		config["mcpServers"] = existing
	}
	delete(config, managedKey)
	return writeJSONObject(path, config)
}

// Names Keydris wrote last time, so a removed server can be cleaned up without
// touching hand-added ones.
const managedKey = "keydrisManagedMcpServers"

func managedNames(config map[string]any) []string {
	raw, _ := config[managedKey].([]any)
	names := make([]string, 0, len(raw))
	for _, value := range raw {
		if name, ok := value.(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	config := map[string]any{}
	if err := json.Unmarshal(data, &config); err != nil {
		// Refuse rather than overwrite: this file holds the user's own MCP
		// servers and project history.
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return config, nil
}

func writeJSONObject(path string, config map[string]any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	// Same-directory temp + rename: a crash must not truncate the user's config.
	temp, err := os.CreateTemp(filepath.Dir(path), ".claude-mcp-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	name := temp.Name()
	if _, err := temp.Write(append(encoded, '\n')); err != nil {
		temp.Close()
		os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

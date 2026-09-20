package sandbox

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// ReadClaudeMCPInventory reads exactly the user configuration managed by
// ConfigureMcpServers. It never launches a server or probes its endpoint.
func ReadClaudeMCPInventory(path string) ([]runtimecontract.MCPInventoryEntry, error) {
	doc, err := readJSONObject(path)
	if err != nil {
		return nil, fmt.Errorf("could not read Claude MCP configuration")
	}
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok && doc["mcpServers"] != nil {
		return nil, fmt.Errorf("invalid Claude MCP configuration")
	}
	managed := map[string]bool{}
	for _, name := range managedNames(doc) {
		managed[name] = true
	}
	entries := make([]runtimecontract.MCPInventoryEntry, 0, len(servers))
	for name, raw := range servers {
		config, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid Claude MCP entry")
		}
		entry := newInventoryEntry(name, managed[name])
		if rawURL, exists := config["url"]; exists {
			value, ok := rawURL.(string)
			if !ok {
				return nil, fmt.Errorf("invalid Claude MCP endpoint")
			}
			setInventoryEndpoint(&entry, value)
		}
		if transport, ok := config["type"].(string); ok {
			if transport == "sse" || transport == "stdio" || transport == "http" {
				entry.Transport = transport
			}
		} else if _, ok := config["command"].(string); ok {
			entry.Transport = "stdio"
		}
		if disabled, ok := config["disabled"].(bool); ok {
			entry.Enabled = !disabled
		}
		entries = append(entries, entry)
	}
	return validateInventory(entries)
}

// ReadCodexMCPInventory reuses the existing TOML document reader, reading only
// top-level MCP table metadata. Nested environment/header tables are ignored.
// Unsupported metadata syntax aborts the snapshot rather than erasing entries.
func ReadCodexMCPInventory(path string) ([]runtimecontract.MCPInventoryEntry, error) {
	doc, err := readTOMLDocument(path)
	if err != nil {
		return nil, fmt.Errorf("could not read Codex MCP configuration")
	}
	managed := map[string]bool{}
	for _, name := range doc.managedNames() {
		managed[name] = true
	}
	entries := make([]runtimecontract.MCPInventoryEntry, 0)
	seen := map[string]bool{}
	for _, section := range doc.sections {
		name, ok := strings.CutPrefix(section.key, codexServerPrefix)
		if !ok {
			if section.key == "mcp_servers" {
				return nil, fmt.Errorf("inline MCP tables are not supported for inventory")
			}
			continue
		}
		// The existing writer normalizes quoted table keys. Nested sections
		// contain secrets and are intentionally not read as server entries.
		if strings.HasSuffix(name, ".env") || strings.HasSuffix(name, ".http_headers") || strings.HasSuffix(name, ".env_http_headers") {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate Codex MCP table")
		}
		seen[name] = true
		entry := newInventoryEntry(name, managed[name])
		for _, line := range section.body {
			key := bareKeyOf(line)
			if key != "url" && key != "command" && key != "enabled" {
				continue
			}
			_, raw, _ := strings.Cut(stripLineComment(line), "=")
			raw = strings.TrimSpace(raw)
			if key == "enabled" {
				if raw != "true" && raw != "false" {
					return nil, fmt.Errorf("invalid Codex MCP enabled flag")
				}
				entry.Enabled = raw == "true"
				continue
			}
			value, err := inventoryTOMLString(raw)
			if err != nil {
				return nil, err
			}
			if key == "url" {
				setInventoryEndpoint(&entry, value)
			} else if entry.Endpoint == nil {
				entry.Transport = "stdio"
			}
		}
		entries = append(entries, entry)
	}
	return validateInventory(entries)
}

func inventoryTOMLString(raw string) (string, error) {
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' && !strings.Contains(raw[1:len(raw)-1], "'") {
		return raw[1 : len(raw)-1], nil
	}
	value, err := strconv.Unquote(raw)
	if err != nil || !strings.HasPrefix(raw, "\"") {
		return "", fmt.Errorf("unsupported Codex MCP metadata syntax")
	}
	return value, nil
}

func newInventoryEntry(name string, managed bool) runtimecontract.MCPInventoryEntry {
	return runtimecontract.MCPInventoryEntry{Name: name, Managed: managed, Enabled: true, Transport: "unknown"}
}

func setInventoryEndpoint(entry *runtimecontract.MCPInventoryEntry, raw string) {
	entry.Transport = "http"
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		entry.EndpointRedacted = true
		return
	}
	entry.EndpointRedacted = endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.ForceQuery
	endpoint.User = nil
	endpoint.RawQuery = ""
	endpoint.ForceQuery = false
	endpoint.Fragment = ""
	endpoint.RawFragment = ""
	value := endpoint.String()
	if len(value) > 2048 {
		entry.EndpointRedacted = true
		return
	}
	entry.Endpoint = &value
}

func validateInventory(entries []runtimecontract.MCPInventoryEntry) ([]runtimecontract.MCPInventoryEntry, error) {
	if len(entries) > 256 {
		return nil, fmt.Errorf("MCP inventory exceeds 256 entries")
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" || len(entry.Name) > 256 || strings.IndexFunc(entry.Name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("invalid MCP entry name")
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

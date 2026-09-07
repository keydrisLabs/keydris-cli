package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Codex reads MCP servers from $CODEX_HOME/config.toml, a file the user also
// owns and comments heavily. So this writer edits it as text, replacing only
// the table sections Keydris owns, rather than round-tripping through a TOML
// marshaller that would delete every comment.
//
// As in the Claude writer, no credential is emitted: the data plane injects the
// upstream token. Names written last time are recorded in a marker comment so a
// narrowed policy can drop a server without touching a hand-added one.
const codexManagedMarker = "# keydris-managed-mcp-servers:"

// Streamable HTTP is behind a Codex feature flag; without it a `url` server is
// ignored rather than dialed.
const (
	codexFeaturesTable = "features"
	codexRmcpKey       = "experimental_use_rmcp_client"
	codexServerPrefix  = "mcp_servers."
)

// ConfigureCodexMcpServers merges Keydris-governed MCP servers into Codex's
// config.toml, preserving hand-added entries and dropping names it managed last
// time but no longer governs.
func ConfigureCodexMcpServers(path string, servers []McpServer) error {
	doc, err := readTOMLDocument(path)
	if err != nil {
		return err
	}

	governed := map[string]bool{}
	for _, server := range servers {
		governed[server.Name] = true
	}
	// Drop both the sections we are about to rewrite and the ones we governed
	// last time but no longer do.
	stale := map[string]bool{}
	for _, name := range doc.managedNames() {
		stale[name] = true
	}
	for name := range governed {
		stale[name] = true
	}
	doc.removeServerSections(stale)

	if len(governed) > 0 {
		doc.setFeatureFlag()
	}
	for _, server := range servers {
		doc.appendSection(codexServerPrefix+server.Name, []string{
			fmt.Sprintf("url = %q", server.URL),
		})
	}
	doc.setManagedNames(sortedNames(governed))
	return writeTOMLDocument(path, doc)
}

// RemoveManagedCodexMcpServers drops every section Keydris added, leaving the
// user's own. Reports whether anything changed; never creates a missing file.
func RemoveManagedCodexMcpServers(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := readTOMLDocument(path)
	if err != nil {
		return false, err
	}
	managed := doc.managedNames()
	if len(managed) == 0 {
		return false, nil
	}
	stale := map[string]bool{}
	for _, name := range managed {
		stale[name] = true
	}
	doc.removeServerSections(stale)
	// The feature flag stays: the user may rely on it for their own servers.
	doc.setManagedNames(nil)
	return true, writeTOMLDocument(path, doc)
}

// ─────────────────────────── document model ───────────────────────────

// tomlSection is one table and the lines that follow it, kept verbatim.
// `header` is empty for the preamble above the first table.
type tomlSection struct {
	// Normalized table key ("mcp_servers.slack-mcp"), for matching.
	key string
	// The raw `[table]` line, preserved byte-for-byte.
	header string
	body   []string
}

type tomlDocument struct {
	sections []*tomlSection
}

// readTOMLDocument splits the file into table sections without interpreting
// values. Lines inside multi-line strings pass through untouched, so a
// bracketed line in one is never mistaken for a table header.
func readTOMLDocument(path string) (*tomlDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &tomlDocument{sections: []*tomlSection{{}}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	doc := &tomlDocument{sections: []*tomlSection{{}}}
	var openDelimiter string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if openDelimiter != "" {
			doc.last().body = append(doc.last().body, line)
			if strings.Contains(line, openDelimiter) {
				openDelimiter = ""
			}
			continue
		}
		if key, ok := tableHeaderKey(line); ok {
			doc.sections = append(doc.sections, &tomlSection{key: key, header: line})
		} else {
			doc.last().body = append(doc.last().body, line)
		}
		openDelimiter = openMultilineDelimiter(line)
	}
	return doc, nil
}

func (d *tomlDocument) last() *tomlSection { return d.sections[len(d.sections)-1] }

// removeServerSections drops `[mcp_servers.<name>]` tables for the given names.
func (d *tomlDocument) removeServerSections(names map[string]bool) {
	kept := make([]*tomlSection, 0, len(d.sections))
	for _, section := range d.sections {
		name, ok := strings.CutPrefix(section.key, codexServerPrefix)
		if ok && names[name] {
			continue
		}
		kept = append(kept, section)
	}
	d.sections = kept
}

// setFeatureFlag turns the RMCP client on, editing an existing `[features]`
// table in place: a second one is invalid TOML and Codex would read none of it.
func (d *tomlDocument) setFeatureFlag() {
	line := codexRmcpKey + " = true"
	for _, section := range d.sections {
		if section.key != codexFeaturesTable {
			continue
		}
		for i, existing := range section.body {
			if bareKeyOf(existing) == codexRmcpKey {
				section.body[i] = line
				return
			}
		}
		section.body = append([]string{line}, section.body...)
		return
	}
	d.appendSection(codexFeaturesTable, []string{line})
}

func (d *tomlDocument) appendSection(key string, body []string) {
	d.sections = append(d.sections, &tomlSection{
		key:    key,
		header: "[" + key + "]",
		body:   body,
	})
}

func (d *tomlDocument) managedNames() []string {
	for _, section := range d.sections {
		for _, line := range section.body {
			rest, ok := strings.CutPrefix(strings.TrimSpace(line), codexManagedMarker)
			if !ok {
				continue
			}
			names := make([]string, 0, 4)
			for _, name := range strings.Split(rest, ",") {
				if name = strings.TrimSpace(name); name != "" {
					names = append(names, name)
				}
			}
			return names
		}
	}
	return nil
}

// setManagedNames rewrites the marker in place, else puts it in the preamble.
func (d *tomlDocument) setManagedNames(names []string) {
	line := codexManagedMarker + " " + strings.Join(names, ",")
	for _, section := range d.sections {
		for i, existing := range section.body {
			if strings.HasPrefix(strings.TrimSpace(existing), codexManagedMarker) {
				if len(names) == 0 {
					section.body = append(section.body[:i:i], section.body[i+1:]...)
				} else {
					section.body[i] = line
				}
				return
			}
		}
	}
	if len(names) == 0 {
		return
	}
	preamble := d.sections[0]
	preamble.body = append([]string{line}, preamble.body...)
}

func (d *tomlDocument) render() string {
	var out strings.Builder
	for _, section := range d.sections {
		if section.header != "" {
			// One blank line before every table, not stacking on repeated writes.
			trimTrailingBlanks(&out)
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(section.header)
			out.WriteString("\n")
		}
		for _, line := range section.body {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	rendered := strings.TrimRight(out.String(), "\n")
	if rendered == "" {
		return ""
	}
	return rendered + "\n"
}

func writeTOMLDocument(path string, doc *tomlDocument) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	// Same-directory temp + rename: a crash must not truncate the user's config.
	temp, err := os.CreateTemp(filepath.Dir(path), ".keydris-codex-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	name := temp.Name()
	if _, err := temp.WriteString(doc.render()); err != nil {
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

// ─────────────────────────── line helpers ───────────────────────────

// tableHeaderKey reports the normalized key of a `[table]` line. Arrays of
// tables (`[[x]]`) are left alone: Keydris writes none.
func tableHeaderKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(stripLineComment(line))
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	if strings.HasPrefix(trimmed, "[[") {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		return "", false
	}
	parts := strings.Split(inner, ".")
	for i, part := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(part), `"'`)
	}
	return strings.Join(parts, "."), true
}

// bareKeyOf returns the key a `key = value` line assigns, or "".
func bareKeyOf(line string) string {
	key, _, found := strings.Cut(stripLineComment(line), "=")
	if !found {
		return ""
	}
	return strings.Trim(strings.TrimSpace(key), `"'`)
}

// stripLineComment drops a trailing `#` comment, ignoring one inside a string.
func stripLineComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
}

// openMultilineDelimiter reports a delimiter the line opens and does not close.
func openMultilineDelimiter(line string) string {
	for _, delimiter := range []string{`"""`, `'''`} {
		if strings.Count(line, delimiter)%2 == 1 {
			return delimiter
		}
	}
	return ""
}

func trimTrailingBlanks(out *strings.Builder) {
	trimmed := strings.TrimRight(out.String(), "\n")
	out.Reset()
	out.WriteString(trimmed)
}

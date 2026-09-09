package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keydrisLabs/keydris-cli/internal/agentguide"
	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
)

const skillStamp = "<!-- keydris-managed-skill sha256:"

func agentSkillPath(cfg *config.Config, target string) (string, error) {
	var root string
	switch target {
	case "claude-code":
		root = filepath.Join(filepath.Dir(cfg.ClaudeSettingsPath), "skills")
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		// Codex's user skill discovery is independent of CODEX_HOME.
		root = filepath.Join(home, ".agents", "skills")
	default:
		return "", fmt.Errorf("unknown skill target %q", target)
	}
	path := filepath.Join(root, "keydris-authority", "SKILL.md")
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("skill location must be an absolute path: %q", path)
	}
	if err := hostenv.Current().ValidatePath("Agent skill", path); err != nil {
		return "", err
	}
	return path, nil
}

func bundledSkill() []byte {
	body := agentguide.Skill + "\nBundled with Keydris CLI " + terminalText(Version) + ".\n"
	return []byte(body + fmt.Sprintf("%s%x -->\n", skillStamp, sha256.Sum256([]byte(body))))
}

// The checksum covers all authored content and the version. A local edit makes
// the file user-owned: neither an upgrade nor deinit may silently replace it.
func isManagedSkill(raw []byte) bool {
	index := bytes.LastIndex(raw, []byte(skillStamp))
	if index < 0 {
		return false
	}
	want := fmt.Sprintf("%s%x -->\n", skillStamp, sha256.Sum256(raw[:index]))
	return string(raw[index:]) == want
}

func readAgentSkill(path string) ([]byte, error) {
	if err := noLinkedPath(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("expected a regular skill file under 1 MiB: %s", path)
	}
	return os.ReadFile(path)
}

func installAgentSkill(path string) error {
	current, err := readAgentSkill(path)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return err
	}
	if !missing && !isManagedSkill(current) {
		return fmt.Errorf("existing or edited skill preserved at %s; move it aside to install the bundled skill", path)
	}
	want := bundledSkill()
	if bytes.Equal(current, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := noLinkedPath(path); err != nil {
		return err
	}
	// Stage updates beside the destination. First installs use exclusive create
	// below so a concurrently created user skill cannot be overwritten.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keydris-skill-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(want); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Preserve a file changed while we prepared the update.
	latest, err := readAgentSkill(path)
	if missing {
		if !os.IsNotExist(err) {
			return fmt.Errorf("skill destination changed during installation: %s", path)
		}
		// Exclusive creation protects a concurrently created user skill.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(want)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, latest) {
		return fmt.Errorf("skill changed during installation: %s", path)
	}
	return os.Rename(tmp.Name(), path)
}

func removeAgentSkill(path string) error {
	raw, err := readAgentSkill(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isManagedSkill(raw) {
		return fmt.Errorf("user-edited skill retained at %s", path)
	}
	// Remove only the owned file; leave the folder and any user additions alone.
	return os.Remove(path)
}

func cleanupAgentSkill(cfg *config.Config, target string) {
	path, err := agentSkillPath(cfg, target)
	// Custom Claude configuration can share Codex's user skill directory.
	// Keep a shared copy while the other integration still uses Keydris.
	other, settings := "codex", cfg.CodexHooksPath
	if target == "codex" {
		other, settings = "claude-code", cfg.ClaudeSettingsPath
	}
	if otherPath, pathErr := agentSkillPath(cfg, other); err == nil && pathErr == nil && pathEqual(path, otherPath) {
		active, inspectErr := sandbox.HasKeydrisHooks(settings)
		if active || inspectErr != nil {
			return
		}
	}
	if err == nil {
		err = removeAgentSkill(path)
	}
	if err != nil {
		newUI(os.Stdout).row("warning", "Agent skill", err.Error())
	}
}

func writeAgentBriefing(w io.Writer, note string) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "SessionStart",
			"additionalContext": strings.TrimSpace(agentguide.Briefing + "\n" + note),
		},
	})
}

// Codex's thread id is unrelated to the Keydris wrapper's session id. This hook
// only supplies context; it must never mint, replace, or end the wrapper session.
func runAgentContext(args []string) int {
	fs := flag.NewFlagSet("__agent-context", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	note := "Codex terminal sessions must launch with keydris codex. VS Code sidebar integration is separate."
	if os.Getenv(sessionOwnerEnv) != sessionOwnerRun || os.Getenv("KEYDRIS_SESSION") == "" {
		note += " This process has no Keydris wrapper session. Tell the user to open a terminal and launch keydris codex before doing governed work."
	}
	if err := writeAgentBriefing(os.Stdout, note); err != nil {
		return 1
	}
	return 0
}

func runSkill(args []string) int {
	fs := flag.NewFlagSet("skill", flag.ContinueOnError)
	brief := fs.Bool("brief", false, "print the short session briefing")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	content := agentguide.Skill
	if *brief {
		content = agentguide.Briefing
	}
	if _, err := io.WriteString(os.Stdout, content); err != nil {
		return 1
	}
	return 0
}

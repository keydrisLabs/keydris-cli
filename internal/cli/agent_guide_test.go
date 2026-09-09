package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentSkillPreservesLocalContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent config with spaces", "skills", "keydris-authority", "SKILL.md")
	if err := installAgentSkill(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !isManagedSkill(first) {
		t.Fatal("installed skill has no valid ownership record")
	}
	if err := installAgentSkill(path); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); !bytes.Equal(raw, first) {
		t.Fatal("repeat install changed the skill")
	}
	oldVersion := Version
	Version = "test-upgrade"
	t.Cleanup(func() { Version = oldVersion })
	if err := installAgentSkill(path); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(path)
	if bytes.Equal(first, updated) || !isManagedSkill(updated) {
		t.Fatal("owned skill was not upgraded")
	}
	custom := append([]byte("user instruction\n"), updated...)
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installAgentSkill(path); err == nil {
		t.Fatal("edited skill was overwritten")
	}
	if err := removeAgentSkill(path); err == nil {
		t.Fatal("edited skill was removed")
	}
	if raw, _ := os.ReadFile(path); !bytes.Equal(raw, custom) {
		t.Fatal("user content changed")
	}
}

func TestAgentSkillResetPreservesChangesAfterPreview(t *testing.T) {
	cfg := uxConfig(t)
	path, err := agentSkillPath(cfg, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := installAgentSkill(path); err != nil {
		t.Fatal(err)
	}
	items, err := planReset(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	var planned *resetTarget
	for i := range items {
		if items[i].path == path {
			planned = &items[i]
		}
	}
	if planned == nil {
		t.Fatal("owned skill missing from reset preview")
	}
	custom := []byte("user edited skill after preview")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeResetTarget(*planned); err == nil {
		t.Fatal("reset removed a changed skill")
	}
	if raw, _ := os.ReadFile(path); !bytes.Equal(raw, custom) {
		t.Fatal("reset changed user content")
	}
}

func TestAgentBriefingDoesNotDiscloseSessionEnvironment(t *testing.T) {
	t.Setenv("KEYDRIS_SESSION", "private-session-marker")
	t.Setenv("HTTPS_PROXY", "http://keydris:private-proxy-token@localhost:15001")
	var out bytes.Buffer
	if err := writeAgentBriefing(&out, ""); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Output.Event != "SessionStart" || payload.Output.Context == "" {
		t.Fatal("missing session context")
	}
	for _, secret := range []string{"private-session-marker", "private-proxy-token", "\x1b"} {
		if bytes.Contains(out.Bytes(), []byte(secret)) {
			t.Fatal("briefing disclosed environment or terminal control")
		}
	}
}

func TestAgentSkillRejectsLinkedDestination(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "user.md")
	if err := os.WriteFile(other, []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "SKILL.md")
	if err := os.Symlink(other, link); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if err := installAgentSkill(link); err == nil {
		t.Fatal("installation followed a symlink")
	}
	if raw, _ := os.ReadFile(other); string(raw) != "user content" {
		t.Fatal("linked user file changed")
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectConfigurationRequiresExplicitTrust(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	home := t.TempDir()
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDir)

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KEYDRIS_CONTROL_URL", "")
	t.Setenv("KEYDRIS_TRUST_PROJECT_CONFIG", "")
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("KEYDRIS_CONTROL_URL=https://untrusted.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loadLayeredFiles()
	if got := os.Getenv("KEYDRIS_CONTROL_URL"); got != "" {
		t.Fatalf("untrusted project config was loaded: %q", got)
	}

	t.Setenv("KEYDRIS_TRUST_PROJECT_CONFIG", "1")
	loadLayeredFiles()
	if got := os.Getenv("KEYDRIS_CONTROL_URL"); got != "https://untrusted.example" {
		t.Fatalf("explicitly trusted project config was not loaded: %q", got)
	}
}

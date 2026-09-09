package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPathsResolveAgainstFileInsteadOfWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, ".keydris.toml")
	t.Setenv("KEYDRIS_DATA_DIR", "")
	if err := os.WriteFile(source, []byte("data_dir = 'my data'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loadToml(source)
	if got, want := os.Getenv("KEYDRIS_DATA_DIR"), filepath.Join(dir, "my data"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

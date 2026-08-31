package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// telemetryTestDataDir isolates config.Load from the developer's real home
// (~/.keydris.toml, ~/.keydris-data) and returns the data dir in use.
func telemetryTestDataDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, "data")
	t.Setenv("KEYDRIS_DATA_DIR", dataDir)
	return dataDir
}

func readTelemetryState(t *testing.T, dataDir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, "telemetry.json"))
	if err != nil {
		t.Fatalf("read telemetry state: %v", err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode telemetry state %q: %v", b, err)
	}
	return st
}

func TestRunTelemetryOffOnPersists(t *testing.T) {
	dataDir := telemetryTestDataDir(t)

	if code := runTelemetry([]string{"off"}); code != 0 {
		t.Fatalf("telemetry off exited %d", code)
	}
	if st := readTelemetryState(t, dataDir); st["opt_out"] != true {
		t.Fatalf("state after off: %v", st)
	}

	if code := runTelemetry([]string{"on"}); code != 0 {
		t.Fatalf("telemetry on exited %d", code)
	}
	if st := readTelemetryState(t, dataDir); st["opt_out"] == true {
		t.Fatalf("state after on: %v", st)
	}
}

func TestRunTelemetryStatusAndUsage(t *testing.T) {
	telemetryTestDataDir(t)

	if code := runTelemetry(nil); code != 0 {
		t.Fatalf("bare telemetry (status) exited %d", code)
	}
	if code := runTelemetry([]string{"status"}); code != 0 {
		t.Fatalf("telemetry status exited %d", code)
	}
	if code := runTelemetry([]string{"bogus"}); code != 1 {
		t.Fatalf("telemetry bogus exited %d, want 1", code)
	}
}

package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

type capturedEvent struct {
	APIKey     string         `json:"api_key"`
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

// startCapture points the package at a test PostHog and returns the received
// events. It also clears every environment gate so each test starts enabled.
func startCapture(t *testing.T) *[]capturedEvent {
	t.Helper()

	var (
		mu     sync.Mutex
		events []capturedEvent
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read capture body: %v", err)
			return
		}
		var ev capturedEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Errorf("decode capture body %q: %v", body, err)
			return
		}
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	prevKey, prevEndpoint := PostHogKey, PostHogEndpoint
	PostHogKey, PostHogEndpoint = "phc_test", srv.URL
	t.Cleanup(func() { PostHogKey, PostHogEndpoint = prevKey, prevEndpoint })

	for _, key := range []string{"DO_NOT_TRACK", "KEYDRIS_TELEMETRY", "KEYDRIS_CHANNEL", "KEYDRIS_DISTRIBUTION"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	return &events
}

func silenceNotice(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := noticeWriter
	noticeWriter = &buf
	t.Cleanup(func() { noticeWriter = prev })
	return &buf
}

func TestFirstRunSendsInstallAndNotice(t *testing.T) {
	events := startCapture(t)
	notice := silenceNotice(t)
	dir := t.TempDir()
	t.Setenv("KEYDRIS_CHANNEL", "stable")
	t.Setenv("KEYDRIS_DISTRIBUTION", "npm")

	RecordRun(dir, "v1.2.3")

	if len(*events) != 1 {
		t.Fatalf("got %d events, want 1", len(*events))
	}
	ev := (*events)[0]
	if ev.Event != "cli_installed" {
		t.Fatalf("event = %q, want cli_installed", ev.Event)
	}
	if ev.APIKey != "phc_test" {
		t.Fatalf("api_key = %q", ev.APIKey)
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(ev.DistinctID) {
		t.Fatalf("distinct_id %q is not a v4 UUID", ev.DistinctID)
	}
	for key, want := range map[string]any{
		"version":                 "v1.2.3",
		"channel":                 "stable",
		"distribution":            "npm",
		"$process_person_profile": false,
	} {
		if got := ev.Properties[key]; got != want {
			t.Errorf("properties[%q] = %v, want %v", key, got, want)
		}
	}
	if notice.Len() == 0 {
		t.Error("first run printed no disclosure notice")
	}
	if id := AnonymousID(dir); id != ev.DistinctID {
		t.Errorf("stored id %q != sent id %q", id, ev.DistinctID)
	}

	// The same version running again reports nothing.
	RecordRun(dir, "v1.2.3")
	if len(*events) != 1 {
		t.Fatalf("second run sent an event: got %d events, want 1", len(*events))
	}
}

func TestUpgradeSendsUpgraded(t *testing.T) {
	events := startCapture(t)
	silenceNotice(t)
	dir := t.TempDir()

	RecordRun(dir, "v1.0.0")
	RecordRun(dir, "v1.1.0")

	if len(*events) != 2 {
		t.Fatalf("got %d events, want 2", len(*events))
	}
	ev := (*events)[1]
	if ev.Event != "cli_upgraded" {
		t.Fatalf("event = %q, want cli_upgraded", ev.Event)
	}
	if got := ev.Properties["previous_version"]; got != "v1.0.0" {
		t.Errorf("previous_version = %v, want v1.0.0", got)
	}
	if got := ev.Properties["version"]; got != "v1.1.0" {
		t.Errorf("version = %v, want v1.1.0", got)
	}
	if ev.DistinctID != (*events)[0].DistinctID {
		t.Errorf("upgrade id %q != install id %q", ev.DistinctID, (*events)[0].DistinctID)
	}

	// Only one notice, on install.
	RecordRun(dir, "v1.1.0")
	if len(*events) != 2 {
		t.Fatalf("steady-state run sent an event: got %d events, want 2", len(*events))
	}
}

func TestOptOutPersistsAndBlocksSending(t *testing.T) {
	events := startCapture(t)
	silenceNotice(t)
	dir := t.TempDir()

	if err := SetOptOut(dir, true); err != nil {
		t.Fatalf("SetOptOut: %v", err)
	}
	RecordRun(dir, "v1.0.0")
	if len(*events) != 0 {
		t.Fatalf("opted-out run sent %d events", len(*events))
	}
	if enabled, reason := Status(dir); enabled || reason == "" {
		t.Fatalf("Status = (%v, %q), want disabled with a reason", enabled, reason)
	}

	// Opting back in before anything was sent reports a fresh install.
	if err := SetOptOut(dir, false); err != nil {
		t.Fatalf("SetOptOut: %v", err)
	}
	RecordRun(dir, "v1.0.0")
	if len(*events) != 1 || (*events)[0].Event != "cli_installed" {
		t.Fatalf("after opting back in got %+v, want one cli_installed", *events)
	}
}

func TestEnvironmentOptOuts(t *testing.T) {
	events := startCapture(t)
	silenceNotice(t)

	t.Run("DO_NOT_TRACK", func(t *testing.T) {
		t.Setenv("DO_NOT_TRACK", "1")
		RecordRun(t.TempDir(), "v1.0.0")
		if len(*events) != 0 {
			t.Fatalf("sent %d events under DO_NOT_TRACK", len(*events))
		}
	})
	t.Run("KEYDRIS_TELEMETRY=off", func(t *testing.T) {
		t.Setenv("KEYDRIS_TELEMETRY", "off")
		RecordRun(t.TempDir(), "v1.0.0")
		if len(*events) != 0 {
			t.Fatalf("sent %d events under KEYDRIS_TELEMETRY=off", len(*events))
		}
	})
}

func TestNoKeyDisablesEverything(t *testing.T) {
	events := startCapture(t)
	silenceNotice(t)
	PostHogKey = ""
	dir := t.TempDir()

	RecordRun(dir, "v1.0.0")
	if len(*events) != 0 {
		t.Fatalf("unkeyed build sent %d events", len(*events))
	}
	if _, err := os.Stat(filepath.Join(dir, stateFile)); !os.IsNotExist(err) {
		t.Fatalf("unkeyed build wrote state (stat err=%v)", err)
	}
	if enabled, _ := Status(dir); enabled {
		t.Fatal("Status reports enabled without a key")
	}
}

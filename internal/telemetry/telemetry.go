// Package telemetry sends anonymous install/upgrade events to PostHog.
//
// It is deliberately minimal and fail-silent: one stdlib HTTP POST per event,
// a short timeout, and no error ever surfaced to the command. Events fire only
// from direct user-facing commands — never from the hook entrypoints or the
// proxy — and only in builds where PostHogKey was stamped at link time, so a
// plain `go build` (and any fork) sends nothing.
package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PostHogKey is the public (write-only) PostHog project API key, stamped at
// link time via -ldflags "-X .../internal/telemetry.PostHogKey=phc_...".
// Empty — the default for source builds — disables telemetry entirely.
var PostHogKey = ""

// PostHogEndpoint is the PostHog ingestion host (US cloud), overridable at
// link time the same way (e.g. to switch clouds or point at a first-party
// relay).
var PostHogEndpoint = "https://us.i.posthog.com"

// noticeWriter receives the one-time disclosure notice.
var noticeWriter io.Writer = os.Stderr

// captureTimeout bounds the whole telemetry request, DNS included.
const captureTimeout = 2 * time.Second

// stateFile is the persisted telemetry state under the data dir. It lives
// there on purpose: ~/.keydris.toml is replaced by install.sh and `keydris
// upgrade` on every config refresh, so an opt-out stored in it would be
// silently reverted.
const stateFile = "telemetry.json"

type state struct {
	// ID is the anonymous install identity: a UUID generated locally on the
	// first send, never derived from hardware, login identity, or the agent.
	ID string `json:"id"`
	// OptOut persists `keydris telemetry off`.
	OptOut bool `json:"opt_out,omitempty"`
	// LastVersion is the version that last reported; a mismatch on a later
	// run means the binary was upgraded (by any install path).
	LastVersion string `json:"last_version,omitempty"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, stateFile) }

func loadState(dataDir string) state {
	var st state
	b, err := os.ReadFile(statePath(dataDir))
	if err != nil {
		return state{}
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return state{}
	}
	return st
}

func saveState(dataDir string, st state) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0o700)
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(dataDir), append(b, '\n'), 0o600)
}

// Status reports whether telemetry is active for this process, and the reason
// when it is not. The checks are ordered so the reason shown is the one the
// user can act on least locally: build, then environment, then persisted
// choice.
func Status(dataDir string) (enabled bool, reason string) {
	if PostHogKey == "" {
		return false, "this build carries no telemetry key; source builds never send"
	}
	if dnt := os.Getenv("DO_NOT_TRACK"); dnt != "" && dnt != "0" {
		return false, "DO_NOT_TRACK is set"
	}
	switch strings.ToLower(os.Getenv("KEYDRIS_TELEMETRY")) {
	case "off", "0", "false", "no", "disabled":
		return false, "KEYDRIS_TELEMETRY is off"
	}
	if loadState(dataDir).OptOut {
		return false, "opted out via `keydris telemetry off`"
	}
	return true, ""
}

// SetOptOut persists the user's choice under dataDir. Identity and version
// state are preserved so re-enabling does not resend an install event.
func SetOptOut(dataDir string, optOut bool) error {
	st := loadState(dataDir)
	st.OptOut = optOut
	return saveState(dataDir, st)
}

// AnonymousID returns the stored install ID, or "" before the first send.
func AnonymousID(dataDir string) string { return loadState(dataDir).ID }

// RecordRun is the single reporting entrypoint, called once per user-facing
// command. It sends cli_installed the first time a keyed build runs and
// cli_upgraded when the version changed since the last report; every other
// run sends nothing.
func RecordRun(dataDir, version string) {
	if enabled, _ := Status(dataDir); !enabled {
		return
	}
	st := loadState(dataDir)
	switch {
	case st.ID == "":
		id, err := newID()
		if err != nil {
			return
		}
		st.ID = id
		st.LastVersion = version
		// Persist before sending: if the state cannot be written, sending
		// anyway would report a fresh install on every subsequent run.
		if err := saveState(dataDir, st); err != nil {
			return
		}
		printNotice()
		capture("cli_installed", st.ID, version, nil)
	case st.LastVersion != version:
		previous := st.LastVersion
		st.LastVersion = version
		if err := saveState(dataDir, st); err != nil {
			return
		}
		capture("cli_upgraded", st.ID, version, map[string]any{"previous_version": previous})
	}
}

// printNotice discloses telemetry on the run that sends the install event,
// with both ways to turn it off.
func printNotice() {
	fmt.Fprint(noticeWriter, "keydris collects anonymous install telemetry: a random ID plus the CLI\n"+
		"version, OS, architecture, release channel, and install method — never code,\n"+
		"commands, prompts, or personal data. Opt out anytime with\n"+
		"`keydris telemetry off` or DO_NOT_TRACK=1.\n"+
		"Details: https://github.com/keydrisLabs/keydris-cli#telemetry\n")
}

// capture posts one event to PostHog's single-event endpoint. Best-effort:
// the response is discarded and every error path returns silently.
func capture(event, distinctID, version string, extra map[string]any) {
	props := map[string]any{
		// Anonymous mode: no person profile is created or updated.
		"$process_person_profile": false,
		"$lib":                    "keydris-cli",
		"version":                 version,
		"os":                      runtime.GOOS,
		"arch":                    runtime.GOARCH,
		// Both env values are seeded by config.Load before RecordRun runs:
		// channel from the installed ~/.keydris.toml, distribution by the npm
		// bin wrapper.
		"channel":      valueOr(os.Getenv("KEYDRIS_CHANNEL"), "unknown"),
		"distribution": valueOr(os.Getenv("KEYDRIS_DISTRIBUTION"), "binary"),
	}
	for k, v := range extra {
		props[k] = v
	}
	payload, err := json.Marshal(map[string]any{
		"api_key":     PostHogKey,
		"event":       event,
		"distinct_id": distinctID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"properties":  props,
	})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: captureTimeout}
	resp, err := client.Post(strings.TrimRight(PostHogEndpoint, "/")+"/i/v0/e/", "application/json", bytes.NewReader(payload))
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func valueOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// newID returns a version-4 UUID from crypto/rand.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

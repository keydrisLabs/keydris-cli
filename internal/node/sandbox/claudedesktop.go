package sandbox

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DesktopEngineOptions drives ApplyDesktopEngineSettings. ProxyURL may include
// userinfo (http://keydris:handle@127.0.0.1:15001); the embedded engine accepts it.
type DesktopEngineOptions struct {
	ProxyURL         string // full URL, may include userinfo: http://keydris:handle@127.0.0.1:15001
	CABundlePath     string
	SessionStartHook string
	SessionEndHook   string
	PreToolUseHook   string
}

var desktopProxyEnvKeys = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

var appliedIDPattern = regexp.MustCompile(`^[a-f0-9-]{36}$`)

// ApplyDesktopEngineSettings merges proxy env, CA env, and the three hooks into
// settingsPath (a settings.json path). It preserves unrelated keys. previous is
// the prior file bytes (nil if the file did not exist).
func ApplyDesktopEngineSettings(settingsPath string, opt DesktopEngineOptions) (previous []byte, existed bool, err error) {
	if strings.TrimSpace(settingsPath) == "" {
		return nil, false, fmt.Errorf("settings path is empty")
	}
	if st, statErr := os.Stat(settingsPath); statErr == nil && st.IsDir() {
		return nil, false, fmt.Errorf("settings path is a directory: %s", settingsPath)
	}

	previous, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return nil, false, readErr
		}
		previous = nil
		existed = false
	} else {
		existed = true
	}

	settings, err := readSettings(settingsPath)
	if err != nil {
		return nil, false, err
	}

	if opt.ProxyURL != "" {
		mergeDesktopProxyEnv(settings, opt.ProxyURL)
	}
	if opt.CABundlePath != "" {
		mergeCAEnv(settings, opt.CABundlePath)
	}
	if opt.SessionStartHook != "" && opt.SessionEndHook != "" {
		mergeHooks(settings, opt.SessionStartHook, opt.SessionEndHook)
	}
	if opt.PreToolUseHook != "" {
		mergePreToolUseHook(settings, opt.PreToolUseHook)
	}

	if err := writeSettings(settingsPath, settings); err != nil {
		return nil, false, err
	}
	return previous, existed, nil
}

// RestoreDesktopEngineSettings writes previous back. If existed is false, it
// removes settingsPath. It does not remove the parent directory.
func RestoreDesktopEngineSettings(settingsPath string, previous []byte, existed bool) error {
	if strings.TrimSpace(settingsPath) == "" {
		return fmt.Errorf("settings path is empty")
	}
	return restoreFile(settingsPath, previous, existed)
}

func mergeDesktopProxyEnv(settings map[string]any, proxyURL string) {
	envBlock, _ := settings["env"].(map[string]any)
	if envBlock == nil {
		envBlock = map[string]any{}
	}
	for _, key := range desktopProxyEnvKeys {
		envBlock[key] = proxyURL
	}
	settings["env"] = envBlock
}

// EgressSnapshot captures the previous configLibrary state so a Desktop session
// can restore the app egress pin on exit.
type EgressSnapshot struct {
	MetaPrevious   []byte
	MetaExisted    bool
	ConfigPrevious []byte
	ConfigExisted  bool
	ConfigName     string // base name, e.g. "<uuid>.json"
}

// ApplyEgressProxyPin sets egressProxyUrl on the applied managed-config document
// in configLibraryDir. Desktop rejects proxy URLs with userinfo, so a proxyURL
// containing '@' is rejected and nothing is written.
func ApplyEgressProxyPin(configLibraryDir, proxyURL string) (EgressSnapshot, error) {
	var snap EgressSnapshot
	if strings.Contains(proxyURL, "@") {
		return snap, fmt.Errorf("egress proxy URL must not contain userinfo")
	}
	if err := os.MkdirAll(configLibraryDir, 0o755); err != nil {
		return snap, err
	}

	metaPath := filepath.Join(configLibraryDir, "_meta.json")
	metaBytes, metaErr := os.ReadFile(metaPath)
	if metaErr == nil {
		snap.MetaPrevious = metaBytes
		snap.MetaExisted = true
	} else if !os.IsNotExist(metaErr) {
		return snap, metaErr
	}

	appliedID := ""
	if snap.MetaExisted {
		var meta map[string]any
		if err := json.Unmarshal(metaBytes, &meta); err == nil {
			if id, ok := meta["appliedId"].(string); ok && appliedIDPattern.MatchString(id) {
				appliedID = id
			}
		}
	}

	if appliedID != "" {
		configName := appliedID + ".json"
		configPath := filepath.Join(configLibraryDir, configName)
		snap.ConfigName = configName

		configBytes, configErr := os.ReadFile(configPath)
		if configErr == nil {
			snap.ConfigPrevious = configBytes
			snap.ConfigExisted = true
		} else if !os.IsNotExist(configErr) {
			return snap, configErr
		}

		doc := map[string]any{}
		if snap.ConfigExisted && len(configBytes) > 0 {
			if err := json.Unmarshal(configBytes, &doc); err != nil {
				return snap, fmt.Errorf("parse existing %s: %w", configPath, err)
			}
		}
		doc["egressProxyUrl"] = proxyURL
		if err := writeSettings(configPath, doc); err != nil {
			return snap, err
		}
		return snap, nil
	}

	id, err := newUUIDv4()
	if err != nil {
		return snap, err
	}
	configName := id + ".json"
	configPath := filepath.Join(configLibraryDir, configName)
	snap.ConfigName = configName
	snap.ConfigExisted = false
	snap.ConfigPrevious = nil

	if err := writeSettings(configPath, map[string]any{"egressProxyUrl": proxyURL}); err != nil {
		return snap, err
	}
	// Desktop's config library code expects entries to list the applied
	// document; it writes the same shape when it creates the library itself.
	meta := map[string]any{
		"appliedId": id,
		"entries":   []any{map[string]any{"id": id, "name": "Keydris session"}},
	}
	if err := writeSettings(metaPath, meta); err != nil {
		return snap, err
	}
	return snap, nil
}

// RestoreEgressProxyPin writes previous meta and config bytes back. Files that
// did not exist before Apply are removed. ConfigName must be set.
func RestoreEgressProxyPin(configLibraryDir string, snapshot EgressSnapshot) error {
	if snapshot.ConfigName == "" {
		return fmt.Errorf("egress snapshot ConfigName is empty")
	}
	metaPath := filepath.Join(configLibraryDir, "_meta.json")
	configPath := filepath.Join(configLibraryDir, snapshot.ConfigName)

	if err := restoreFile(metaPath, snapshot.MetaPrevious, snapshot.MetaExisted); err != nil {
		return err
	}
	return restoreFile(configPath, snapshot.ConfigPrevious, snapshot.ConfigExisted)
}

func restoreFile(path string, previous []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, previous, 0o644)
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

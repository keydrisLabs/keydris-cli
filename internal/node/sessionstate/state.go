// Package sessionstate owns the durable, owner-only state shared by the short-
// lived command hooks and the long-running proxy daemon.
package sessionstate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

type State struct {
	SessionID     string                         `json:"session_id"`
	Handle        string                         `json:"handle"`
	ULID          string                         `json:"ulid"`
	SPIFFEID      string                         `json:"spiffe_id"`
	Blueprint     string                         `json:"blueprint"`
	ExpiresAt     string                         `json:"expires_at"`
	OwnerPID      int                            `json:"owner_pid,omitempty"`
	OwnerManaged  bool                           `json:"owner_managed,omitempty"`
	OwnerIdentity string                         `json:"owner_identity,omitempty"`
	KIT           string                         `json:"kit,omitempty"`
	Routes        *runtimecontract.RuntimeRoutes `json:"routes"`
}

func Dir(dataDir string) string { return filepath.Join(dataDir, "sessions") }

func Path(dataDir, sessionID string) (string, error) {
	if err := ValidateID(sessionID); err != nil {
		return "", err
	}
	return filepath.Join(Dir(dataDir), sessionID+".json"), nil
}

func ValidateID(sessionID string) error {
	if sessionID == "" || len(sessionID) > 128 {
		return fmt.Errorf("invalid session id")
	}
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("invalid session id %q", sessionID)
		}
	}
	if sessionID == "." || sessionID == ".." {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	return nil
}

// Save replaces a state file atomically so a concurrent hook observes either
// the previous complete credential or the renewed complete credential, never a
// partially written JSON document.
func Save(dataDir string, state State) error {
	path, err := Path(dataDir, state.SessionID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)

	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, "."+state.SessionID+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicReplace(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func Load(dataDir, sessionID string) (State, error) {
	var state State
	path, err := Path(dataDir, sessionID)
	if err != nil {
		return state, err
	}
	file, err := os.Open(path)
	if err != nil {
		return state, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	if err := decoder.Decode(&state); err != nil {
		return State{}, err
	}
	if state.SessionID != sessionID {
		return State{}, fmt.Errorf("session state id mismatch")
	}
	return state, nil
}

func Remove(dataDir, sessionID string) error {
	path, err := Path(dataDir, sessionID)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

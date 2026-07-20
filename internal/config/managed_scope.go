package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

const managedScopeFilename = "managed-destinations.json"

type ManagedScopeState struct {
	Mode         string   `json:"mode"`
	Destinations []string `json:"destinations"`
}

func managedScopePath(dataDir string) string {
	return filepath.Join(dataDir, managedScopeFilename)
}

func loadManagedScope(dataDir string) (ManagedScopeState, error) {
	state, err := ReadManagedScope(dataDir)
	if err != nil {
		return ManagedScopeState{Mode: proxyscope.ModeSelected}, err
	}
	return state, nil
}

// ReadManagedScope reads the persisted proxy scope. A missing file means all
// destinations are managed, preserving the historical behavior.
func ReadManagedScope(dataDir string) (ManagedScopeState, error) {
	state := ManagedScopeState{Mode: proxyscope.ModeAll}
	body, err := os.ReadFile(managedScopePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return ManagedScopeState{}, err
	}
	scope, err := proxyscope.New(state.Mode, state.Destinations)
	if err != nil {
		return ManagedScopeState{}, err
	}
	return ManagedScopeState{Mode: scope.Mode(), Destinations: scope.Destinations()}, nil
}

// SaveManagedScope validates and persists the effective selected origins.
func SaveManagedScope(dataDir, mode string, destinations []string) error {
	scope, err := proxyscope.New(mode, destinations)
	if err != nil {
		return err
	}
	state := ManagedScopeState{Mode: scope.Mode(), Destinations: scope.Destinations()}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0o700)
	path := managedScopePath(dataDir)
	tmp, err := os.CreateTemp(dataDir, managedScopeFilename+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

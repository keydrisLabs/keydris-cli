package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

const managedScopeFilename = "managed-destinations.json"

// ManagedScopeSourcePolicy marks a scope derived from the agent's policy.
const ManagedScopeSourcePolicy = "policy"

type ManagedScopeState struct {
	Mode string `json:"mode"`
	// Source is empty on files written before auto-detection.
	Source       string   `json:"source,omitempty"`
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
	return ManagedScopeState{
		Mode:         scope.Mode(),
		Source:       state.Source,
		Destinations: scope.Destinations(),
	}, nil
}

// SaveDerivedManagedScope persists the policy's origins as the effective
// scope. No origins means selected-with-none (manage nothing), unlike a
// missing file, which still means manage everything.
func SaveDerivedManagedScope(dataDir string, destinations []string) error {
	return saveManagedScope(
		dataDir, proxyscope.ModeSelected, ManagedScopeSourcePolicy, destinations,
	)
}

// RemoveManagedScope deletes the derived scope cache; missing is not an error.
func RemoveManagedScope(dataDir string) error {
	if err := os.Remove(managedScopePath(dataDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// saveManagedScope validates and atomically writes the scope file.
func saveManagedScope(dataDir, mode, source string, destinations []string) error {
	scope, err := proxyscope.New(mode, destinations)
	if err != nil {
		return err
	}
	state := ManagedScopeState{
		Mode:         scope.Mode(),
		Source:       source,
		Destinations: scope.Destinations(),
	}
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

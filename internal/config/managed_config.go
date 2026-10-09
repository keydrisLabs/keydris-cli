package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func defaultManagedConfigPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/Keydris/config.toml"
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "Keydris", "config.toml")
	default:
		return "/etc/keydris/config.toml"
	}
}

// validateManagedConfig rejects links, non-root ownership, and any file or
// containing directory writable by group/other. A missing file is normal for
// unmanaged installations.
func validateManagedConfig(path string, expectedOwnerUID uint32) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("managed config %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("managed config %s must be a regular file, not a link", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("managed config %s is writable by group or other users", path)
	}
	if err := requireFileOwner(info, expectedOwnerUID); err != nil {
		return fmt.Errorf("managed config %s: %w", path, err)
	}

	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("managed config directory %s: %w", parent, err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return fmt.Errorf("managed config directory %s must be a directory, not a link", parent)
	}
	if parentInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("managed config directory %s is writable by group or other users", parent)
	}
	if err := requireFileOwner(parentInfo, expectedOwnerUID); err != nil {
		return fmt.Errorf("managed config directory %s: %w", parent, err)
	}
	return nil
}

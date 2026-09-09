package cli

import (
	"fmt"
	"github.com/keydrisLabs/keydris-cli/internal/config"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func pathEqual(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func checkResetInProgress(cfg *config.Config) error {
	path := filepath.Join(cfg.DataDir, ".reset-in-progress")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("Keydris reset is in progress; retry when it finishes (if interrupted, inspect %s)", path)
}
func withinPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Reject links at every existing component, including Windows junctions.
// Missing descendants are safe to plan; validate again immediately before use.
func noLinkedPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %s", path)
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			linked, err := pathIsLink(current, info)
			if err != nil {
				return err
			}
			if linked {
				return fmt.Errorf("refusing symlink or junction: %s", current)
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
func validateResetRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("reset data directory must be absolute")
	}
	root = filepath.Clean(root)
	if filepath.Dir(root) == root {
		return fmt.Errorf("refusing to reset a filesystem root")
	}
	for _, get := range []func() (string, error){os.UserHomeDir, os.Getwd, os.Executable} {
		protected, err := get()
		if err != nil {
			return err
		}
		if withinPath(root, protected) {
			return fmt.Errorf("refusing to reset a home, working directory, executable, or its ancestor: %s", root)
		}
	}
	return noLinkedPath(root)
}
func validateResetTree(path string) error {
	if err := noLinkedPath(path); err != nil {
		return err
	}
	err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !withinPath(path, current) {
			return fmt.Errorf("reset path escaped its planned root")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		linked, err := pathIsLink(current, info)
		if err != nil {
			return err
		}
		if linked {
			return fmt.Errorf("refusing symlink or junction: %s", current)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

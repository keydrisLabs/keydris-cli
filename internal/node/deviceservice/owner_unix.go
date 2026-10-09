//go:build !windows

package deviceservice

import (
	"fmt"
	"os"
	"syscall"
)

func validateOwnedPath(path string, expectedUID uint32, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect protected path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("protected path %s must not be a symlink", path)
	}
	if directory != info.IsDir() {
		return fmt.Errorf("protected path %s has the wrong file type", path)
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("protected path %s must be a regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != expectedUID {
		return fmt.Errorf("protected path %s must be owned by uid %d", path, expectedUID)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("protected path %s must not be accessible by group or other users", path)
	}
	return nil
}

func validateStateParent(path string, expectedUID uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("device service state parent must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != expectedUID {
		return fmt.Errorf("device service state parent must be owned by uid %d", expectedUID)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("device service state parent must not be group or other writable")
	}
	return nil
}

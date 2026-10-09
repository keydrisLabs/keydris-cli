//go:build !windows

package deviceservice

import (
	"fmt"
	"os"
	"syscall"
)

func validateSocketDirectory(path string, expectedUID uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("device service socket parent must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != expectedUID {
		return fmt.Errorf("device service socket parent must be owned by uid %d", expectedUID)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("device service socket parent must not be group or other writable")
	}
	if info.Mode().Perm()&0o111 != 0o111 {
		return fmt.Errorf("device service socket parent must be traversable by local users")
	}
	return nil
}

func validateSocketOwner(info os.FileInfo, expectedUID uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != expectedUID {
		return fmt.Errorf("stale device service socket must be owned by uid %d", expectedUID)
	}
	return nil
}

func secureServiceSocket(path string, expectedUID uint32) error {
	if err := os.Chown(path, int(expectedUID), -1); err != nil {
		return fmt.Errorf("set device service socket owner: %w", err)
	}
	// All local users may connect, but the service obtains their UID from the
	// kernel and binds every signature to it. No request-supplied identity is
	// trusted, and root-only mutations are checked after peer resolution.
	if err := os.Chmod(path, 0o666); err != nil {
		return fmt.Errorf("set device service socket permissions: %w", err)
	}
	return nil
}

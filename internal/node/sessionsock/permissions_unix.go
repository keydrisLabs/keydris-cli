//go:build !windows

package sessionsock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func secureSocket(path string) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine session socket owner")
	}
	socketInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	socketStat, ok := socketInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine current session socket owner")
	}
	if socketStat.Uid != stat.Uid || socketStat.Gid != stat.Gid {
		if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
			return fmt.Errorf("set session socket owner: %w", err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set session socket permissions: %w", err)
	}
	return nil
}

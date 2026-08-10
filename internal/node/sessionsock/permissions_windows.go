//go:build windows

package sessionsock

import "os"

func secureSocket(path string) error {
	return os.Chmod(path, 0o600)
}

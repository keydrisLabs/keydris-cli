//go:build windows

package deviceservice

import (
	"fmt"
	"os"
)

func validateOwnedPath(path string, _ uint32, _ bool) error {
	if _, err := os.Lstat(path); err != nil {
		return fmt.Errorf("inspect protected path %s: %w", path, err)
	}
	return fmt.Errorf("device service ACL validation is not available on Windows yet")
}

func validateStateParent(path string, _ uint32) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	return fmt.Errorf("device service ACL validation is not available on Windows yet")
}

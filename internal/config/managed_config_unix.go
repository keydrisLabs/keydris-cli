//go:build darwin || linux

package config

import (
	"fmt"
	"os"
	"syscall"
)

func requireFileOwner(info os.FileInfo, expectedOwnerUID uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine owner")
	}
	if stat.Uid != expectedOwnerUID {
		return fmt.Errorf("must be owned by uid %d (found %d)", expectedOwnerUID, stat.Uid)
	}
	return nil
}

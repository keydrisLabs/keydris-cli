//go:build windows

package config

import (
	"fmt"
	"os"
)

func requireFileOwner(_ os.FileInfo, _ uint32) error {
	return fmt.Errorf("owner validation is not available on Windows yet")
}

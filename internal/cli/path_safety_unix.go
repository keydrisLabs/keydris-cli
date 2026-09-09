//go:build !windows

package cli

import "os"

func pathIsLink(_ string, info os.FileInfo) (bool, error) {
	return info.Mode()&os.ModeSymlink != 0, nil
}

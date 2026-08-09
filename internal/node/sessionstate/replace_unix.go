//go:build !windows

package sessionstate

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}

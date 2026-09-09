//go:build !linux && !darwin && !windows

package cli

import "os"

func terminalFile(f *os.File) bool { return false }

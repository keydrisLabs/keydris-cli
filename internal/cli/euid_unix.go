//go:build !windows

package cli

import "os"

func effectiveUID() int { return os.Geteuid() }

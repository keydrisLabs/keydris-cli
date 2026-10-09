package config

import (
	"os"
	"path/filepath"
	"runtime"
)

func defaultDeviceServiceDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/Keydris/device"
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "Keydris", "device")
	default:
		return "/var/lib/keydris/device"
	}
}

func defaultDeviceServiceSocket() string {
	switch runtime.GOOS {
	case "darwin":
		return "/var/run/keydris/device.sock"
	case "windows":
		return `\\.\pipe\keydris-device`
	default:
		return "/run/keydris/device.sock"
	}
}

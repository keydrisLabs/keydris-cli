package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWSLRuntimeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		os, release, distro, interop, want string
		valid                              bool
	}{
		{"linux", "6.6.87.2-microsoft-standard-WSL2", "Ubuntu", "/run/WSL/123_interop", "2", true},
		{"linux", "4.4.0-19041-Microsoft", "Ubuntu", "", "1", false},
		{"linux", "6.12-custom", "Ubuntu", "/run/WSL/123_interop", "unknown", false},
		{"windows", "", "Ubuntu", "", "unknown", false},
		{"linux", "6.12-generic", "", "", "", true},
		{"windows", "", "", "", "", true},
	} {
		e := Detect(tc.os, tc.release, tc.distro, tc.interop, "vscode")
		if e.WSL != tc.want || (e.Validate() == nil) != tc.valid || !e.VSCode {
			t.Errorf("unexpected runtime for %+v: %+v", tc, e)
		}
	}
}

func TestWSLCustomWindowsMounts(t *testing.T) {
	mounts := windowsMounts("36 25 0:32 / /windows/Drive\\040C rw - 9p C:\\ rw,aname=drvfs;path=C:\\;uid=1000\n37 25 0:33 / /projects rw - ext4 /dev/sdc rw\n")
	e := Environment{OS: "linux", WSL: "2", windowsMounts: mounts}
	for _, path := range []string{"/mnt/c/state", "/windows/Drive C/state", "/mnt/d"} {
		if !e.isWindowsPath(path) {
			t.Errorf("Windows mount accepted: %s", path)
		}
	}
	for _, path := range []string{"/home/user/state", "/projects/app", "/mnt/data", "/windows/Drive Copy"} {
		if e.isWindowsPath(path) {
			t.Errorf("Linux path rejected: %s", path)
		}
	}
}

func TestWSLDetectsLinkedStateAncestor(t *testing.T) {
	dir := t.TempDir()
	windows := filepath.Join(dir, "windows drive")
	if err := os.Mkdir(windows, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linux-looking-home")
	if err := os.Symlink(windows, link); err != nil {
		t.Skip("symlink creation unavailable")
	}
	e := Environment{OS: "linux", WSL: "2", windowsMounts: []string{filepath.ToSlash(windows)}}
	if err := e.ValidatePath("data", filepath.Join(link, "not-created", "state")); err == nil {
		t.Fatal("linked Windows state ancestor accepted")
	}
}

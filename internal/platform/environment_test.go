package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	windows, err := filepath.EvalSymlinks(windows)
	if err != nil {
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

func TestWSLCommandFileFormats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	e := Environment{OS: "linux", WSL: "2"}
	for _, tc := range []struct {
		name, content, wantError string
	}{
		{"claude.exe", "\x7fELF\x02\x01\x01\x00", ""},
		{"native.EXE", "\x7fELF\x02\x01\x01\x00", ""},
		{"native", "\x7fELF\x02\x01\x01\x00", ""},
		{"windows.exe", "MZ windows executable", "Windows binary"},
		{"renamed-windows", "MZ windows executable", "Windows binary"},
		{"unknown.exe", "unknown executable", "Windows executable"},
		{"truncated.exe", "\x7fEL", "Windows executable"},
		{"empty.exe", "", "Windows executable"},
		{"launcher.cmd", "@echo off\r\n", "Windows executable"},
		{"launcher.bat", "@echo off\r\n", "Windows executable"},
		{"launcher.ps1", "Write-Host 'hello'\n", "Windows executable"},
		{"launcher", "#!/bin/sh\necho hello\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(executable, []byte(tc.content), 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := e.resolveCommand(executable)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got %q, %v; want error containing %q", got, err, tc.wantError)
				}
			} else if err != nil || got != executable {
				t.Fatalf("got %q, %v; want %q", got, err, executable)
			}
		})
	}
}

func TestWSLCommandResolvesSymlinkBeforeCheckingFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude.exe")
	if err := os.WriteFile(binary, []byte("\x7fELF\x02\x01\x01\x00"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "claude")
	if err := os.Symlink(binary, launcher); err != nil {
		t.Skip("symlink creation unavailable")
	}
	e := Environment{OS: "linux", WSL: "2"}
	if got, err := e.resolveCommand(launcher); err != nil || got != launcher {
		t.Fatalf("Linux npm symlink rejected: %q, %v", got, err)
	}
	if err := os.WriteFile(binary, []byte("MZ windows executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolveCommand(launcher); err == nil {
		t.Fatal("symlink to Windows binary accepted")
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.windowsMounts = []string{filepath.ToSlash(resolvedDir)}
	if err := os.WriteFile(binary, []byte("\x7fELF\x02\x01\x01\x00"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolveCommand(launcher); err == nil || !strings.Contains(err.Error(), "Windows-mounted path") {
		t.Fatalf("Windows-mounted ELF accepted: %v", err)
	}
}

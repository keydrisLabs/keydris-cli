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
		// The header check precedes the extension switch: an MZ payload is
		// reported as a Windows binary even behind an interop shim suffix.
		{"windows.cmd", "MZ windows executable", "Windows binary"},
		{"unknown.exe", "unknown executable", "Windows executable"},
		// Only ELF earns the .exe exemption; a script that would be a valid
		// POSIX launcher is still a Windows shim when named .exe.
		{"script.exe", "#!/usr/bin/env node\n", "Windows executable"},
		{"truncated.exe", "\x7fEL", "Windows executable"},
		{"empty.exe", "", "Windows executable"},
		{"launcher.cmd", "@echo off\r\n", "Windows executable"},
		{"launcher.bat", "@echo off\r\n", "Windows executable"},
		{"launcher.com", "unknown executable", "Windows executable"},
		{"launcher.ps1", "Write-Host 'hello'\n", "Windows executable"},
		// The ELF exemption is for Claude Code's bin/claude.exe only: other
		// Windows suffixes are rejected regardless of content.
		{"mismatched.cmd", "\x7fELF\x02\x01\x01\x00", "Windows executable"},
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

// TestWSLCommandUnreadableExecutableFailsClosed pins the ordering introduced
// with the .exe ELF exemption: a Windows-suffixed executable is inspected
// before it can be trusted, so a file that cannot be read is refused instead
// of being classified from its suffix alone.
func TestWSLCommandUnreadableExecutableFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the read permission this test relies on")
	}
	executable := filepath.Join(t.TempDir(), "claude.exe")
	if err := os.WriteFile(executable, []byte("\x7fELF\x02\x01\x01\x00"), 0o111); err != nil {
		t.Fatal(err)
	}
	e := Environment{OS: "linux", WSL: "2"}
	got, err := e.resolveCommand(executable)
	if err == nil {
		t.Fatalf("unreadable executable accepted: %q", got)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("resolveCommand() error = %v, want a read failure", err)
	}
	if strings.Contains(err.Error(), "install its Linux version") {
		t.Fatalf("unreadable executable classified without reading it: %v", err)
	}
}

// TestWSLCommandNodeScriptDoesNotRecurse covers the guard that keeps the
// recursive Linux-Node probe from re-resolving the node command itself when
// node is a node-shebang script.
func TestWSLCommandNodeScriptDoesNotRecurse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	if err := os.WriteFile(node, []byte("#!/usr/bin/env node\nconsole.log('node')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	e := Environment{OS: "linux", WSL: "2"}
	got, err := e.resolveCommand("node")
	if err != nil || got != node {
		t.Fatalf("resolveCommand(node) = %q, %v; want %q without re-resolution", got, err, node)
	}
}

func TestWSLCommandNodeLauncherNeedsLinuxNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "claude")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\nconsole.log('lint')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Environment{OS: "linux", WSL: "2"}
	t.Setenv("PATH", dir) // no node on PATH
	if _, err := e.resolveCommand(launcher); err == nil || !strings.Contains(err.Error(), "needs Linux Node.js") {
		t.Fatalf("node launcher without Linux Node accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := e.resolveCommand(launcher); err != nil || got != launcher {
		t.Fatalf("node launcher with Linux Node rejected: %q, %v", got, err)
	}
}

func TestResolveCommandSkipsFormatChecksOutsideWSL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows shim lookup differs")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "tool.cmd")
	if err := os.WriteFile(shim, []byte("not a Windows executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Environment{OS: runtime.GOOS}
	if got, err := e.resolveCommand(shim); err != nil || got != shim {
		t.Fatalf("non-WSL resolution rejected %q: %q, %v", shim, got, err)
	}
}

func TestResolveCommandUsesCurrentEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	got, err := ResolveCommand("sh")
	if err != nil || !filepath.IsAbs(got) {
		t.Fatalf("ResolveCommand(sh) = %q, %v; want an absolute path", got, err)
	}
}

// TestWSLCommandResolutionRejectsUnsupportedEnvironment verifies that command
// resolution fails closed on a Windows-hosted or WSL1 environment before any
// PATH lookup, so an interop shim is never considered launchable there.
func TestWSLCommandResolutionRejectsUnsupportedEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    Environment
		want string
	}{
		{"windows host under WSL", Environment{OS: "windows", WSL: "2"}, "launched from WSL"},
		{"WSL1", Environment{OS: "linux", WSL: "1"}, "requires WSL2"},
		{"unknown WSL", Environment{OS: "linux", WSL: "unknown"}, "requires WSL2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := tc.e.resolveCommand("claude"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolveCommand(claude) = %q, %v; want error containing %q", got, err, tc.want)
			}
		})
	}
}

// TestWSLCommandNodeResolutionKeepsEnvironmentMounts guards recursive node
// resolution using the same Environment: a Linux launcher whose node resolves
// onto a Windows mount must be refused. That only holds because the nested
// lookup goes through the receiver instead of re-detecting the host.
func TestWSLCommandNodeResolutionKeepsEnvironmentMounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL uses Unix executable lookup")
	}
	launcherDir := t.TempDir()
	nodeDir := t.TempDir()
	launcher := filepath.Join(launcherDir, "claude")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\nconsole.log('lint')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "node"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedNodeDir, err := filepath.EvalSymlinks(nodeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", launcherDir+string(os.PathListSeparator)+nodeDir)
	e := Environment{OS: "linux", WSL: "2", windowsMounts: []string{filepath.ToSlash(resolvedNodeDir)}}
	if _, err := e.resolveCommand(launcher); err == nil || !strings.Contains(err.Error(), "needs Linux Node.js") || !strings.Contains(err.Error(), "Windows-mounted path") {
		t.Fatalf("node on a Windows mount was not refused: %v", err)
	}
}

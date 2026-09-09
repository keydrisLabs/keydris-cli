// Package platform identifies the host execution environment without launching
// shells or depending on the agent's working directory.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Environment struct {
	OS            string
	WSL           string // empty, "1", "2", or "unknown"
	Distro        string
	VSCode        bool
	windowsMounts []string
}

func Current() Environment {
	release, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	e := Detect(runtime.GOOS, string(release), os.Getenv("WSL_DISTRO_NAME"), os.Getenv("WSL_INTEROP"), os.Getenv("TERM_PROGRAM"))
	if e.OS == "linux" && e.WSL != "" {
		mounts, _ := os.ReadFile("/proc/self/mountinfo")
		e.windowsMounts = windowsMounts(string(mounts))
	}
	return e
}

func Detect(goos, release, distro, interop, terminal string) Environment {
	e := Environment{OS: goos, Distro: distro, VSCode: terminal == "vscode"}
	release = strings.ToLower(release)
	switch {
	case goos == "linux" && (strings.Contains(release, "wsl2") || strings.Contains(release, "microsoft-standard")):
		e.WSL = "2"
	case goos == "linux" && strings.Contains(release, "microsoft"):
		e.WSL = "1"
	case distro != "" || interop != "":
		e.WSL = "unknown"
	}
	return e
}

func (e Environment) Validate() error {
	if e.WSL == "" {
		return nil
	}
	if e.OS != "linux" {
		return fmt.Errorf("Windows Keydris is being launched from WSL; install and run the Linux CLI using Linux Node/npm inside the distribution")
	}
	if e.WSL != "2" {
		return fmt.Errorf("Keydris requires WSL2; this environment reports WSL%s. Check wsl.exe --list --verbose from Windows", e.WSL)
	}
	return nil
}

func (e Environment) ValidatePath(name, path string) error {
	if e.WSL == "" || e.OS != "linux" {
		return nil
	}
	// Resolve existing ancestors as well as the final file. A not-yet-created
	// state directory can still sit below a symlink onto a Windows drive.
	resolved, err := physicalPath(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", name, err)
	}
	if e.isWindowsPath(resolved) {
		return fmt.Errorf("%s resolves to a Windows-mounted path (%s); keep Keydris state and agent configuration in the WSL Linux home, separate from Windows", name, resolved)
	}
	return nil
}

func physicalPath(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		if _, err := os.Lstat(current); err == nil {
			base, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			rest, err := filepath.Rel(current, path)
			if err != nil {
				return "", err
			}
			return filepath.Join(base, rest), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if current == parent {
			return "", fmt.Errorf("cannot resolve path %s", path)
		}
		current = parent
	}
}

func (e Environment) isWindowsPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	// Cover the default WSL automount location even when mountinfo is hidden.
	if len(path) >= 6 && strings.HasPrefix(path, "/mnt/") && path[5] >= 'a' && path[5] <= 'z' && (len(path) == 6 || path[6] == '/') {
		return true
	}
	for _, mount := range e.windowsMounts {
		if path == mount || strings.HasPrefix(path, strings.TrimSuffix(mount, "/")+"/") {
			return true
		}
	}
	return false
}

func windowsMounts(mountinfo string) []string {
	var mounts []string
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 5 || len(right) < 3 {
			continue
		}
		if right[0] == "drvfs" || right[0] == "9p" && strings.Contains(right[2], "aname=drvfs") {
			mounts = append(mounts, decode.Replace(left[4]))
		}
	}
	return mounts
}

// ResolveCommand freezes PATH resolution before a session is minted and
// refuses the Windows executables/shims that WSL interop can otherwise launch.
func ResolveCommand(command string) (string, error) {
	e := Current()
	if err := e.Validate(); err != nil {
		return "", err
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if e.WSL == "" {
		return path, nil
	}
	resolved, err := physicalPath(path)
	if err != nil {
		return "", err
	}
	if err := e.ValidatePath(command, resolved); err != nil {
		return "", err
	}
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".exe", ".cmd", ".bat", ".com", ".ps1":
		return "", fmt.Errorf("%s resolves to a Windows executable (%s); install its Linux version inside WSL", command, resolved)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	var header [256]byte
	n, readErr := f.Read(header[:])
	f.Close()
	if n >= 2 && string(header[:2]) == "MZ" {
		return "", fmt.Errorf("%s is a Windows binary; install its Linux version inside WSL", resolved)
	}
	// npm's POSIX launchers can still inherit Windows Node through PATH.
	if readErr == nil && strings.HasPrefix(string(header[:n]), "#!") && strings.Contains(strings.SplitN(string(header[:n]), "\n", 2)[0], "node") && command != "node" {
		if _, err := ResolveCommand("node"); err != nil {
			return "", fmt.Errorf("%s needs Linux Node.js: %w", command, err)
		}
	}
	return path, nil
}

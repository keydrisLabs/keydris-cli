package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// defaultBaseURL is where release artifacts live; it mirrors install.sh and can
// be overridden with KEYDRIS_BASE_URL or --base-url.
const defaultBaseURL = "https://dev.get.keydris.com/keydris-cli"

// runUpgrade downloads the latest `keydris` binary for the selected channel,
// verifies its checksum, and atomically replaces the running executable in
// place. It is the in-CLI equivalent of re-running install.sh, so an operator
// can `keydris upgrade` instead of piping curl to bash.
//
// On the dev channel it also refreshes ~/.keydris.toml (backing up the old one)
// so endpoint/client-id changes shipped in the release are actually picked up —
// install.sh deliberately never clobbers that file, which is why a plain
// re-install keeps stale config.
func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	channel := fs.String("channel", envOr("KEYDRIS_CHANNEL", "stable"), "release channel: stable|dev")
	version := fs.String("version", envOr("KEYDRIS_VERSION", "latest"), "version to install (default: latest)")
	baseURL := fs.String("base-url", envOr("KEYDRIS_BASE_URL", defaultBaseURL), "download base URL")
	noConfig := fs.Bool("no-config", false, "do not refresh ~/.keydris.toml on the dev channel")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	osName, arch, err := platform()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: %v\n", err)
		return 1
	}

	name := fmt.Sprintf("keydris-%s-%s", osName, arch)
	base := strings.TrimRight(*baseURL, "/")
	verdir := fmt.Sprintf("%s/%s/%s", base, *channel, *version)

	fmt.Fprintf(os.Stderr, "==> downloading %s (%s/%s)\n", name, *channel, *version)
	binBytes, err := httpGet(verdir + "/" + name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: download binary: %v\n", err)
		return 1
	}
	sums, err := httpGet(verdir + "/SHA256SUMS")
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: download checksums: %v\n", err)
		return 1
	}

	fmt.Fprintln(os.Stderr, "==> verifying checksum")
	expected := checksumFor(sums, name)
	if expected == "" {
		fmt.Fprintf(os.Stderr, "keydris upgrade: no checksum for %s in SHA256SUMS\n", name)
		return 1
	}
	sum := sha256.Sum256(binBytes)
	if actual := hex.EncodeToString(sum[:]); actual != expected {
		fmt.Fprintf(os.Stderr, "keydris upgrade: checksum mismatch for %s (expected %s, got %s)\n", name, expected, actual)
		return 1
	}

	target, err := selfPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: locate current binary: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "==> installing to %s\n", target)
	if err := replaceExecutable(target, binBytes); err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: %v\n", err)
		return 1
	}

	if strings.EqualFold(*channel, "dev") && !*noConfig {
		refreshDevConfig(base, *version)
	}

	fmt.Fprintf(os.Stderr, "==> done (was %s); run `keydris version` to confirm\n", Version)
	return 0
}

// platform maps the Go runtime to the {os, arch} used in release artifact names,
// rejecting combinations we do not publish.
func platform() (osName, arch string, err error) {
	osName = runtime.GOOS
	if osName != "darwin" && osName != "linux" {
		return "", "", fmt.Errorf("unsupported OS %q (supported: darwin, linux)", osName)
	}
	arch = runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return "", "", fmt.Errorf("unsupported arch %q (supported: amd64, arm64)", arch)
	}
	return osName, arch, nil
}

// selfPath resolves the path of the running executable, following symlinks so we
// replace the real file rather than a symlink into it.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// replaceExecutable writes the new binary next to the target and atomically
// renames it into place (POSIX allows replacing the file of a running process).
// A same-directory temp file keeps the rename on one filesystem so it is atomic.
func replaceExecutable(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".keydris-upgrade-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write to %s: permission denied (try `sudo keydris upgrade`)", dir)
		}
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("finalize new binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		cleanup()
		if os.IsPermission(err) {
			return fmt.Errorf("cannot replace %s: permission denied (try `sudo keydris upgrade`)", target)
		}
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// refreshDevConfig re-fetches the dev channel's ~/.keydris.toml so endpoint and
// client-id changes land on upgrade, backing up any existing file first. It is
// best-effort: the binary is already upgraded, so a config hiccup is a warning,
// not a failure.
func refreshDevConfig(base, version string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		fmt.Fprintf(os.Stderr, "==> skipping config refresh: cannot resolve home dir\n")
		return
	}
	dst := filepath.Join(home, ".keydris.toml")

	data, err := httpGet(fmt.Sprintf("%s/dev/%s/keydris.toml", base, version))
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> WARNING: could not fetch dev config (%v); left %s unchanged\n", err, dst)
		return
	}
	if existing, err := os.ReadFile(dst); err == nil {
		if bytes.Equal(existing, data) {
			fmt.Fprintf(os.Stderr, "==> dev config already up to date (%s)\n", dst)
			return
		}
		bak := dst + ".bak"
		if err := os.WriteFile(bak, existing, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "==> WARNING: could not back up %s (%v); left it unchanged\n", dst, err)
			return
		}
		fmt.Fprintf(os.Stderr, "==> backed up existing config to %s\n", bak)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "==> WARNING: could not write %s (%v)\n", dst, err)
		return
	}
	fmt.Fprintf(os.Stderr, "==> refreshed dev config at %s\n", dst)
}

// checksumFor returns the hex checksum for name from an SHA256SUMS body (lines
// of "<hex>  <filename>"), or "" if absent.
func checksumFor(sums []byte, name string) string {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[len(fields)-1] == name {
			return fields[0]
		}
	}
	return ""
}

// httpGet fetches a URL and returns its body, erroring on any non-200 status.
func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// envOr returns the environment value for key, or def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

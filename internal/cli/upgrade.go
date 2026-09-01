package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
)

// channelBaseURL maps a release channel to the only host that serves it,
// mirroring what scripts/render-install.sh bakes into each published install.sh.
// Overridden as a whole by KEYDRIS_BASE_URL or --base-url.
var channelBaseURL = map[string]string{
	"stable": "https://get.keydris.com/keydris-cli",
	"dev":    "https://dev.get.keydris.com/keydris-cli",
}

const (
	maxUpgradeBinary = 256 << 20
	maxChecksumFile  = 1 << 20
	maxChannelConfig = 4 << 20
)

// runUpgrade downloads the latest `keydris` binary for the selected channel,
// verifies its checksum, and atomically replaces the running executable in
// place. It is the in-CLI equivalent of re-running install.sh, so an operator
// can `keydris upgrade` instead of piping curl to bash.
//
// It also refreshes ~/.keydris.toml (backing up the old one) so endpoint and
// client-id changes shipped in the release are actually picked up — install.sh
// deliberately never clobbers that file, which is why a plain re-install keeps
// stale config.
//
// The channel defaults to the one this installation came from: the channel's
// keydris.toml carries `channel = "..."`, which config.Load maps to
// KEYDRIS_CHANNEL. Without it a dev install would silently upgrade onto stable —
// a different binary *and* a different control plane.
func runUpgrade(args []string) int {
	config.Load()

	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	channel := fs.String("channel", envOr("KEYDRIS_CHANNEL", "stable"), "release channel: stable|dev")
	version := fs.String("version", envOr("KEYDRIS_VERSION", "latest"), "version to install (default: latest)")
	// Resolved after parsing: a flag default cannot depend on another flag.
	baseURL := fs.String("base-url", envOr("KEYDRIS_BASE_URL", ""), "download base URL (default: the channel's host)")
	noConfig := fs.Bool("no-config", false, "do not refresh ~/.keydris.toml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if printNPMUpgradeInstructions(os.Stderr) {
		return 0
	}
	if *channel != "stable" && *channel != "dev" {
		fmt.Fprintln(os.Stderr, "keydris upgrade: channel must be stable or dev")
		return 1
	}
	if !safeArtifactSegment(*version) {
		fmt.Fprintf(os.Stderr, "keydris upgrade: invalid version %q\n", *version)
		return 1
	}
	*baseURL = baseURLFor(*channel, *baseURL)

	osName, arch, err := platform()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: %v\n", err)
		return 1
	}

	name := fmt.Sprintf("keydris-%s-%s", osName, arch)
	base := strings.TrimRight(*baseURL, "/")
	verdir := fmt.Sprintf("%s/%s/%s", base, *channel, *version)

	fmt.Fprintf(os.Stderr, "==> downloading %s (%s/%s)\n", name, *channel, *version)
	binBytes, err := httpGetLimit(verdir+"/"+name, maxUpgradeBinary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris upgrade: download binary: %v\n", err)
		return 1
	}
	sums, err := httpGetLimit(verdir+"/SHA256SUMS", maxChecksumFile)
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

	if !*noConfig {
		refreshChannelConfig(base, *channel, *version)
	}

	fmt.Fprintf(os.Stderr, "==> done (was %s); run `keydris version` to confirm\n", Version)
	return 0
}

// baseURLFor resolves the download base for a channel. An explicit override wins
// so mirrors and local test servers still work.
func baseURLFor(channel, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	return channelBaseURL[channel]
}

func printNPMUpgradeInstructions(w io.Writer) bool {
	if os.Getenv("KEYDRIS_DISTRIBUTION") != "npm" {
		return false
	}
	fmt.Fprintln(w, "keydris: this installation is managed by npm")
	fmt.Fprintln(w, "upgrade globally with:")
	fmt.Fprintln(w, "  npm install --global @keydris/cli@latest")
	fmt.Fprintln(w, "or update a project dependency with:")
	fmt.Fprintln(w, "  npm install --save-dev @keydris/cli@latest")
	return true
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

// refreshChannelConfig re-fetches the channel's ~/.keydris.toml so endpoint and
// client-id changes land on upgrade, backing up any existing file first. It is
// best-effort: the binary is already upgraded, so a config hiccup is a warning,
// not a failure. It runs for both channels, as install.sh does.
func refreshChannelConfig(base, channel, version string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		fmt.Fprintf(os.Stderr, "==> skipping config refresh: cannot resolve home dir\n")
		return
	}
	dst := filepath.Join(home, ".keydris.toml")

	data, err := httpGetLimit(fmt.Sprintf("%s/%s/%s/keydris.toml", base, channel, version), maxChannelConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> WARNING: could not fetch %s config (%v); left %s unchanged\n", channel, err, dst)
		return
	}
	if existing, err := os.ReadFile(dst); err == nil {
		if bytes.Equal(existing, data) {
			fmt.Fprintf(os.Stderr, "==> %s config already up to date (%s)\n", channel, dst)
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
	fmt.Fprintf(os.Stderr, "==> refreshed %s config at %s\n", channel, dst)
}

// checksumFor returns the hex checksum for name from an SHA256SUMS body (lines
// of "<hex>  <filename>"), or "" if absent.
func checksumFor(sums []byte, name string) string {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[len(fields)-1] == name {
			candidate := strings.ToLower(fields[0])
			if len(candidate) != sha256.Size*2 {
				return ""
			}
			if _, err := hex.DecodeString(candidate); err != nil {
				return ""
			}
			return candidate
		}
	}
	return ""
}

// httpGetLimit fetches a release artifact over HTTPS with a strict body bound.
// Plain HTTP is accepted only for a loopback test/development server.
func httpGetLimit(rawURL string, maxBytes int64) ([]byte, error) {
	if err := validateUpgradeURL(rawURL); err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return validateUpgradeRedirect(req, via)
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", rawURL, maxBytes)
	}
	return body, nil
}

func validateUpgradeRedirect(req *http.Request, via []*http.Request) error {
	for _, previous := range via {
		if previous.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing HTTPS downgrade redirect to %s", req.URL)
		}
	}
	return validateUpgradeURL(req.URL.String())
}

func validateUpgradeURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid download URL %q", rawURL)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme == "http" && (strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("refusing insecure download URL %q (HTTPS required)", rawURL)
}

func safeArtifactSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') &&
			r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// envOr returns the environment value for key, or def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

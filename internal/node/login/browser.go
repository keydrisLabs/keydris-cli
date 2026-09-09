package login

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/platform"
)

// openBrowser launches the default browser at url. Best-effort: callers fall
// back to printing the URL when this returns an error.
func openBrowser(address string) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("browser sign-in requires an HTTP(S) URL")
	}
	if environment := platform.Current(); environment.OS == "linux" && environment.WSL != "" {
		return openWSLBrowser(address)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", address)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	// Reap the child so we don't leave a zombie; the browser detaches anyway.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Browser handoff is the deliberate Windows interop exception. Pass the URL
// as one argument, never through cmd.exe or a shell (OAuth URLs contain '&').
// A nonzero opener exit triggers the next fallback and eventually the existing
// manual-URL flow. No opener can consume the entire login timeout.
func openWSLBrowser(address string) error {
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{"wslview", []string{address}},
		{"rundll32.exe", []string{"url.dll,FileProtocolHandler", address}},
		{"/mnt/c/Windows/System32/rundll32.exe", []string{"url.dll,FileProtocolHandler", address}},
		{"xdg-open", []string{address}},
	} {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		err = exec.CommandContext(ctx, path, candidate.args...).Run()
		cancel()
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("WSL browser handoff unavailable; open the sign-in URL in your Windows browser")
}

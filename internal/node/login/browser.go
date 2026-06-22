package login

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser launches the default browser at url. Best-effort: callers fall
// back to printing the URL when this returns an error.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	// Reap the child so we don't leave a zombie; the browser detaches anyway.
	go func() { _ = cmd.Wait() }()
	return nil
}

//go:build !linux

package cli

// On non-Linux platforms there is no cgroup binding; the session handle is a
// synthetic token and connection attribution is unavailable (the proxy-env
// fallback enforces destination-only policy). This keeps the hook usable for
// development on macOS.
func bindSession(sessionID string) (string, error) {
	return "/keydris/" + sessionID, nil
}

func unbindSession(string) error { return nil }

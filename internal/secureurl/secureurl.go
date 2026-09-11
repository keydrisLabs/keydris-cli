// Package secureurl guards the channels that carry tokens or credentials:
// a control-plane or runtime URL must be https unless it points at loopback.
//
// Loopback never leaves the machine, so plaintext is acceptable there — and
// only there. The default `http://127.0.0.1:8081` dev control plane keeps
// working with zero friction; a remote `http://` override is refused at the
// moment a token would be sent to it, with an error naming the fix.
// KEYDRIS_ALLOW_INSECURE_CONTROL=1 is the explicit lab-only escape hatch,
// mirroring the control plane's own MCP_ALLOW_INSECURE_URLS precedent.
package secureurl

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// AllowInsecureEnv is the escape hatch consulted by Assert.
const AllowInsecureEnv = "KEYDRIS_ALLOW_INSECURE_CONTROL"

// Assert returns nil when raw is safe to send a token or credential to:
// https anywhere, http only to loopback (or with the escape hatch set).
// name labels the URL in the error (the config variable it came from).
func Assert(name, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s %q is not a valid URL", name, raw)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		if os.Getenv(AllowInsecureEnv) == "1" {
			return nil
		}
		return fmt.Errorf(
			"%s %q is plaintext http to a non-loopback host; tokens and credentials travel on this channel — use https, or set %s=1 for a lab setup",
			name, raw, AllowInsecureEnv,
		)
	default:
		return fmt.Errorf("%s %q must be an http(s) URL", name, raw)
	}
}

func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

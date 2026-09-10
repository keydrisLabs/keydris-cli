package login

import (
	"runtime"

	"github.com/keydrisLabs/keydris-cli/internal/platform"
)

// ClientVersion is the CLI build version reported to the control plane on
// enrollment and renewal. The cli package sets it from its link-time stamped
// Version at init; "dev" is what an unstamped build reports.
var ClientVersion = "dev"

// clientMetadata is the self-reported "where does this CLI run" block sent on
// /identity/sign, /identity/enroll and /identity/renew. The control plane
// stores it on the device so the console can show a platform mark and per-CLI
// version facts; every field is optional server-side, so an older control
// plane simply ignores them.
func clientMetadata() map[string]string {
	fields := map[string]string{
		"platform":    runtime.GOOS,
		"arch":        runtime.GOARCH,
		"cli_version": ClientVersion,
	}
	if environment := platform.Current(); environment.WSL != "" {
		// The Linux CLI inside Windows Subsystem for Linux: platform stays
		// "linux" (that is what runs), the variant tells the operator where.
		fields["platform_variant"] = "wsl" + environment.WSL
	}
	return fields
}

// withClientMetadata copies the metadata into an existing request payload.
func withClientMetadata(payload map[string]string) map[string]string {
	for key, value := range clientMetadata() {
		payload[key] = value
	}
	return payload
}

package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// CA trust for the agent's sandboxed subprocess tools.
//
// The Keydris proxy terminates TLS with a leaf signed by the Keydris CA, so the
// tools the agent runs inside the sandbox (curl, git, npm, node, ...) must trust
// that CA or every HTTPS call fails verification. Two layers cover this:
//
//  1. Per-tool env (the portable default, applied via the settings.json `env`
//     block in claudecode.go): each ecosystem reads a different variable, so we
//     set all the common ones to the Keydris CA path.
//  2. OS trust store (optional, see catrust_<os>.go): a stronger, system-wide
//     install for tools that ignore the env vars. It is best-effort and may need
//     privileges, so it is gated behind `keydris init --trust-store`.

// caEnvKeys lists the environment variables the common sandboxed tools read to
// locate an extra CA bundle. All are set to the Keydris CA path.
func caEnvKeys() []string {
	return []string{
		"NODE_EXTRA_CA_CERTS", // node / npm / claude-code itself
		"SSL_CERT_FILE",       // openssl, python (certifi-aware builds), many CLIs
		"CURL_CA_BUNDLE",      // curl
		"GIT_SSL_CAINFO",      // git
		"REQUESTS_CA_BUNDLE",  // python requests
	}
}

// BuildCABundle combines the platform's PEM root bundle with the Keydris CA so
// replacement-style environment variables keep trusting unmanaged public TLS.
func BuildCABundle(caPath, bundlePath string) error {
	keydrisCA, err := os.ReadFile(caPath)
	if err != nil {
		return fmt.Errorf("read Keydris CA: %w", err)
	}

	candidates := []string{
		os.Getenv("SSL_CERT_FILE"),
		"/etc/ssl/cert.pem",
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
	}
	var systemRoots []byte
	for _, candidate := range candidates {
		if candidate == "" || samePath(candidate, caPath) || samePath(candidate, bundlePath) {
			continue
		}
		body, readErr := os.ReadFile(candidate)
		if readErr == nil && bytes.Contains(body, []byte("-----BEGIN CERTIFICATE-----")) {
			systemRoots = body
			break
		}
	}
	if len(systemRoots) == 0 {
		return fmt.Errorf("no system PEM root bundle found; set SSL_CERT_FILE to the platform root bundle")
	}

	bundle := append([]byte(nil), bytes.TrimSpace(systemRoots)...)
	bundle = append(bundle, '\n')
	bundle = append(bundle, bytes.TrimSpace(keydrisCA)...)
	bundle = append(bundle, '\n')
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(bundlePath), 0o700)
	if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
		return err
	}
	return os.Chmod(bundlePath, 0o600)
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// InstallTrustStore installs the CA at caPath into the OS trust store. It is
// best-effort and platform-specific; the env-var layer in mergeCAEnv is the
// portable default and is always applied by Configure.
func InstallTrustStore(caPath string) error {
	return installTrustStore(caPath)
}

package sandbox

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

// InstallTrustStore installs the CA at caPath into the OS trust store. It is
// best-effort and platform-specific; the env-var layer in mergeCAEnv is the
// portable default and is always applied by Configure.
func InstallTrustStore(caPath string) error {
	return installTrustStore(caPath)
}
